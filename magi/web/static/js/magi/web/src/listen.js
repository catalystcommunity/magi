// Hands-free talk (docs/speech-interaction.md, 2.3.1, steps 1 and 2). The
// microphone stays on, with the echo canceller of the browser. The voice
// activity detector (vad.ts) finds when the person speaks:
//   - while the persona speaks, an onset lowers its volume at once. Then
//     Magi decides from the words (check-overlap: speech check, STT, and a
//     small classifier with what the persona said last): "stop" stops the
//     persona, and the speech becomes the next turn of the person;
//     "continue" (a backchannel such as "mm-hm", "right, okay") restores
//     the volume, and the speech is not a turn. Speech that goes on for
//     2.5 s is a turn in any case. Without check-overlap (an older Magi, or
//     the engine is down), speech of 700 ms stops the persona;
//   - at the end of a turn, the audio (WAV, 16 kHz) goes to Magi.
// The recording starts 400 ms before the onset, so the first sound is not
// lost.
import { createSignal } from "solid-js";
import { Vad, defaultVad } from "./vad.js";
import { audioContext, unlock, duck, speaking, interrupt, heardUntil } from "./speech.js";
import { magi, RpcError } from "./api.js";
export const [handsFree, setHandsFree] = createSignal(false);
export const [hearing, setHearing] = createSignal(false); // the person speaks now
// The words of the person while the persona speaks, as Magi heard them.
export const [overlapWords, setOverlapWords] = createSignal("");
const prerollMs = 400;
const maxTurnMs = 60_000;
const firstCheckMs = 400; // speech before the first check
const checkEveryMs = 300; // then a check this often, one at a time
const longTurnMs = 2500; // speech this long takes the turn
let overlapAvailable = true; // false after check-overlap is not there
let stream = null;
let node = null;
let sink = null;
export async function startListening(onTurn) {
    unlock();
    const ctx = audioContext();
    if (!ctx)
        throw new Error("This browser cannot record audio.");
    stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
    await ctx.audioWorklet.addModule(new URL("./capture-worklet.js", import.meta.url));
    const src = ctx.createMediaStreamSource(stream);
    node = new AudioWorkletNode(ctx, "magi-capture");
    sink = ctx.createGain();
    sink.gain.value = 0; // the worklet runs only when it is connected
    src.connect(node).connect(sink).connect(ctx.destination);
    const rate = ctx.sampleRate;
    const frameLen = Math.round(rate * defaultVad.frameMs / 1000);
    const vad = new Vad();
    let pending = new Float32Array(0);
    const preroll = [];
    let rec = null;
    let ov = null; // the persona spoke at the onset
    let elapsed = 0; // ms of audio so far
    const takeTurn = (o) => {
        if (o.stopped)
            return;
        o.stopped = true;
        duck(1);
        interrupt("interrupted");
    };
    const check = (o, final, frames) => {
        o.busy = true;
        o.lastMs = elapsed;
        const audio = wav(frames, rate);
        return magi.checkOverlap({ message: o.message, audio, audioType: "audio/wav", heardUntil: heardUntil(), final })
            .then((r) => {
            if (ov !== o && !final)
                return r.decision;
            setOverlapWords(r.words);
            if (r.decision === "stop")
                takeTurn(o);
            else if (r.decision === "continue")
                duck(1);
            return r.decision;
        })
            .catch((e) => {
            // Not there (404, 501) or the engine is down (503): the simple rule.
            if (e instanceof RpcError && [404, 501, 503].includes(e.code))
                overlapAvailable = false;
            return "error";
        })
            .finally(() => { o.busy = false; });
    };
    node.port.onmessage = (e) => {
        const joined = new Float32Array(pending.length + e.data.length);
        joined.set(pending);
        joined.set(e.data, pending.length);
        let at = 0;
        for (; at + frameLen <= joined.length; at += frameLen) {
            const frame = joined.slice(at, at + frameLen);
            elapsed += defaultVad.frameMs;
            if (rec)
                rec.push(frame);
            else {
                preroll.push(frame);
                if (preroll.length * defaultVad.frameMs > prerollMs)
                    preroll.shift();
            }
            vad.extraDb = speaking() ? 6 : 0; // what is left of the echo
            const ev = vad.push(frame);
            switch (ev) {
                case "onset": {
                    const m = speaking();
                    ov = m ? { message: m, busy: false, lastMs: 0, stopped: false } : null;
                    if (ov)
                        duck(0.25);
                    setOverlapWords("");
                    rec = preroll.splice(0);
                    setHearing(true);
                    break;
                }
                case "end": {
                    const o = ov, frames = rec;
                    ov = null;
                    rec = null;
                    setHearing(false);
                    if (!frames)
                        break;
                    if (!o || o.stopped) {
                        duck(1);
                        onTurn(wav(frames, rate));
                        break;
                    }
                    if (!overlapAvailable) {
                        // Shorter than a turn: a backchannel; the persona goes on.
                        duck(1);
                        break;
                    }
                    // The person stopped speaking: the last check decides.
                    check(o, true, frames).then((d) => {
                        if (d === "stop")
                            onTurn(wav(frames, rate));
                        else
                            duck(1);
                    });
                    break;
                }
                case "cancel":
                    ov = null;
                    duck(1);
                    rec = null;
                    setHearing(false);
                    break;
            }
            if (ov && rec && !ov.stopped) {
                if (vad.speechMs >= (overlapAvailable ? longTurnMs : defaultVad.turnMs))
                    takeTurn(ov);
                else if (!ov.busy && vad.speechMs >= firstCheckMs && elapsed - ov.lastMs >= checkEveryMs)
                    check(ov, false, rec.slice());
            }
            if (rec && rec.length * defaultVad.frameMs > maxTurnMs) {
                onTurn(wav(rec, rate));
                rec = null;
                ov = null;
                setHearing(false);
            }
        }
        pending = joined.slice(at);
    };
    setHandsFree(true);
}
export function stopListening() {
    stream?.getTracks().forEach((t) => t.stop());
    node?.disconnect();
    sink?.disconnect();
    stream = null;
    node = null;
    sink = null;
    setHandsFree(false);
    setHearing(false);
    duck(1);
}
// wav makes a 16 kHz 16-bit mono WAV file of the frames.
export function wav(frames, rate) {
    const n = frames.reduce((s, f) => s + f.length, 0);
    const all = new Float32Array(n);
    let o = 0;
    for (const f of frames) {
        all.set(f, o);
        o += f.length;
    }
    const outRate = 16000;
    const len = Math.floor(n * outRate / rate);
    const buf = new DataView(new ArrayBuffer(44 + len * 2));
    const str = (at, s) => { for (let i = 0; i < s.length; i++)
        buf.setUint8(at + i, s.charCodeAt(i)); };
    str(0, "RIFF");
    buf.setUint32(4, 36 + len * 2, true);
    str(8, "WAVEfmt ");
    buf.setUint32(16, 16, true);
    buf.setUint16(20, 1, true);
    buf.setUint16(22, 1, true);
    buf.setUint32(24, outRate, true);
    buf.setUint32(28, outRate * 2, true);
    buf.setUint16(32, 2, true);
    buf.setUint16(34, 16, true);
    str(36, "data");
    buf.setUint32(40, len * 2, true);
    const step = rate / outRate;
    for (let i = 0; i < len; i++) {
        // The mean of the samples of each output sample (a simple low pass).
        const a = Math.floor(i * step), b = Math.max(a + 1, Math.floor((i + 1) * step));
        let sum = 0;
        for (let j = a; j < b && j < n; j++)
            sum += all[j];
        const v = Math.max(-1, Math.min(1, sum / (b - a)));
        buf.setInt16(44 + i * 2, v < 0 ? v * 0x8000 : v * 0x7fff, true);
    }
    return new Uint8Array(buf.buffer);
}
