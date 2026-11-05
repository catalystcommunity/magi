// Hands-free talk (docs/speech-interaction.md, 2.3.1, steps 1 and 2). The
// microphone stays on, with the echo canceller of the browser. The voice
// activity detector (vad.ts) finds when the person speaks:
//   - while the persona speaks, an onset lowers its volume at once; speech
//     that goes on for 700 ms stops the persona, and becomes the next turn
//     of the person; shorter speech ("mm-hm") only restores the volume;
//   - at the end of a turn, the audio (WAV, 16 kHz) goes to Magi.
// The recording starts 400 ms before the onset, so the first sound is not
// lost.
import { createSignal } from "solid-js";
import { Vad, defaultVad } from "./vad.ts";
import { audioContext, unlock, duck, speaking, interrupt } from "./speech.ts";

export const [handsFree, setHandsFree] = createSignal(false);
export const [hearing, setHearing] = createSignal(false); // the person speaks now

const prerollMs = 400;
const maxTurnMs = 60_000;

let stream: MediaStream | null = null;
let node: AudioWorkletNode | null = null;
let sink: GainNode | null = null;

// startListening turns on the microphone. onTurn gets the audio of each
// turn of the person.
export async function startListening(onTurn: (wav: Uint8Array) => void) {
  unlock();
  const ctx = audioContext();
  if (!ctx) throw new Error("This browser cannot record audio.");
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
  const preroll: Float32Array[] = [];
  let rec: Float32Array[] | null = null;
  let duringSpeech = false; // the persona spoke at the onset

  node.port.onmessage = (e: MessageEvent<Float32Array>) => {
    const joined = new Float32Array(pending.length + e.data.length);
    joined.set(pending);
    joined.set(e.data, pending.length);
    let at = 0;
    for (; at + frameLen <= joined.length; at += frameLen) {
      const frame = joined.slice(at, at + frameLen);
      if (rec) rec.push(frame);
      else {
        preroll.push(frame);
        if (preroll.length * defaultVad.frameMs > prerollMs) preroll.shift();
      }
      vad.extraDb = speaking() ? 6 : 0; // what is left of the echo
      switch (vad.push(frame)) {
        case "onset":
          duringSpeech = speaking() !== null;
          if (duringSpeech) duck(0.25);
          rec = preroll.splice(0);
          setHearing(true);
          break;
        case "turn":
          if (duringSpeech) interrupt("interrupted");
          duringSpeech = false;
          break;
        case "end":
          if (!duringSpeech && rec) onTurn(wav(rec, rate));
          duck(1);
          rec = null;
          setHearing(false);
          break;
        case "cancel":
          duck(1);
          rec = null;
          setHearing(false);
          break;
      }
      if (rec && rec.length * defaultVad.frameMs > maxTurnMs) {
        onTurn(wav(rec, rate));
        rec = null;
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
  stream = null; node = null; sink = null;
  setHandsFree(false);
  setHearing(false);
  duck(1);
}

// wav makes a 16 kHz 16-bit mono WAV file of the frames.
export function wav(frames: Float32Array[], rate: number): Uint8Array {
  const n = frames.reduce((s, f) => s + f.length, 0);
  const all = new Float32Array(n);
  let o = 0;
  for (const f of frames) { all.set(f, o); o += f.length; }
  const outRate = 16000;
  const len = Math.floor(n * outRate / rate);
  const buf = new DataView(new ArrayBuffer(44 + len * 2));
  const str = (at: number, s: string) => { for (let i = 0; i < s.length; i++) buf.setUint8(at + i, s.charCodeAt(i)); };
  str(0, "RIFF"); buf.setUint32(4, 36 + len * 2, true); str(8, "WAVEfmt ");
  buf.setUint32(16, 16, true); buf.setUint16(20, 1, true); buf.setUint16(22, 1, true);
  buf.setUint32(24, outRate, true); buf.setUint32(28, outRate * 2, true); buf.setUint16(32, 2, true); buf.setUint16(34, 16, true);
  str(36, "data"); buf.setUint32(40, len * 2, true);
  const step = rate / outRate;
  for (let i = 0; i < len; i++) {
    // The mean of the samples of each output sample (a simple low pass).
    const a = Math.floor(i * step), b = Math.max(a + 1, Math.floor((i + 1) * step));
    let sum = 0;
    for (let j = a; j < b && j < n; j++) sum += all[j];
    const v = Math.max(-1, Math.min(1, sum / (b - a)));
    buf.setInt16(44 + i * 2, v < 0 ? v * 0x8000 : v * 0x7fff, true);
  }
  return new Uint8Array(buf.buffer);
}
