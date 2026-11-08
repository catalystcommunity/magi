// Spoken replies in parts (contracts/magi.csil, listen). The player gets
// the parts of one message as Magi makes them, plays them one after the
// other with Web Audio, and knows which word the person hears now (the
// spoken cursor). When playback ends or stops, it reports the delivery, so
// that the conversation graph knows how much of the reply the person heard.
import { createSignal } from "solid-js";
import type { Message, SpeechPart } from "../../../clients/typescript/magi/types.gen.ts";
import { magi, errText, notify } from "./api.ts";

// The message that is spoken now, and the word that the person hears
// (code point offsets in the message text).
export const [speaking, setSpeaking] = createSignal<string | null>(null);
export const [word, setWord] = createSignal<{ start: number; end: number } | null>(null);

let ctx: AudioContext | null = null;
let out: GainNode | null = null; // all speech plays through it (duck)

// audioContext is the audio context of the page, after unlock.
export const audioContext = () => ctx;

// duck sets the volume of the speech: 1, or less while the person may be
// taking the turn.
export function duck(level: number) {
  if (ctx && out) out.gain.setTargetAtTime(level, ctx.currentTime, 0.03);
}

// unlock makes the audio context in a click or a key press: browsers play
// audio only after the person did something on the page.
export function unlock() {
  try {
    if (!ctx) {
      ctx = new AudioContext();
      out = ctx.createGain();
      out.connect(ctx.destination);
    }
    if (ctx.state === "suspended") ctx.resume();
  } catch { /* no Web Audio */ }
}

type Scheduled = { part: SpeechPart; at: number; source: AudioBufferSourceNode };

class Playback {
  scheduled: Scheduled[] = [];
  end = 0;           // audio context time when the last scheduled part ends
  heard = 0;         // the offset in the text up to which the person heard
  over = false;
  frame = 0;
  constructor(public m: Pick<Message, "id" | "text">, public shownMs: number, public onEnd?: (reason: string) => void) {}

  async run() {
    let after = 0;
    let done = false;
    let stopped = false;
    while (!this.over && !done) {
      const r = await magi.listen({ message: this.m.id, after, waitMs: 20000 });
      for (const p of r.parts) {
        if (this.over) return;
        await this.schedule(p);
      }
      after += r.parts.length;
      done = r.done;
      stopped = r.stopped;
    }
    if (this.over) return;
    if (after === 0) { this.finish(stopped ? "user" : "error"); return; }
    // Wait for the end of the last part.
    const left = Math.max(0, this.end - ctx!.currentTime);
    setTimeout(() => this.finish("done"), left * 1000 + 50);
  }

  async schedule(p: SpeechPart) {
    const a = await magi.getAudio({ digest: p.audio });
    const buf = await ctx!.decodeAudioData(a.body.slice().buffer as ArrayBuffer);
    if (this.over) return;
    const source = ctx!.createBufferSource();
    source.buffer = buf;
    source.connect(out!);
    const at = Math.max(ctx!.currentTime + 0.03, this.end);
    source.start(at);
    this.end = at + buf.duration;
    this.scheduled.push({ part: p, at, source });
    if (!this.frame) this.tick();
  }

  // tick follows the spoken cursor once a frame.
  tick = () => {
    if (this.over) return;
    const now = ctx!.currentTime;
    let current: { start: number; end: number } | null = null;
    for (const s of this.scheduled) {
      if (now < s.at) break;
      const ms = (now - s.at) * 1000;
      for (const w of s.part.words) {
        if (w.startMs > ms) break;
        current = { start: w.start, end: w.end };
        this.heard = Math.max(this.heard, w.end);
      }
    }
    const old = word();
    if (current?.start !== old?.start || current?.end !== old?.end) setWord(current);
    this.frame = requestAnimationFrame(this.tick);
  };

  stop(reason: string) {
    if (this.over) return;
    for (const s of this.scheduled) { try { s.source.stop(); } catch { /* not started */ } }
    magi.stopSpeech({ message: this.m.id, reason }).catch(() => { /* the parts may be made already */ });
    this.finish(reason);
  }

  finish(reason: string) {
    if (this.over) return;
    this.over = true;
    cancelAnimationFrame(this.frame);
    if (current === this) { current = null; setSpeaking(null); setWord(null); }
    this.onEnd?.(reason);
    const len = Array.from(this.m.text).length;
    const spokenUntil = reason === "done" ? len : this.heard;
    magi.reportDelivery({
      message: this.m.id, spokenUntil, shownMs: this.shownMs, reason,
      stoppedMs: reason === "done" ? undefined : Date.now(),
    }).catch(() => { /* the report is optional */ });
  }
}

let current: Playback | null = null;

// speak plays the parts of a persona message (or of the message of a
// notice). shownMs is when the client showed its text. onEnd gets the
// reason when the playback ends ("done" when it played to the end).
export function speak(m: Pick<Message, "id" | "text">, shownMs = Date.now(), onEnd?: (reason: string) => void) {
  unlock();
  if (!ctx) { notify("This browser cannot play audio.", true); onEnd?.("error"); return; }
  current?.stop("user");
  duck(1);
  const p = new Playback(m, shownMs, onEnd);
  current = p;
  setSpeaking(m.id);
  p.run().catch((e) => { notify("Could not play the reply: " + errText(e), true); p.finish("error"); });
}

// interrupt stops the speech when the person takes the turn: they type,
// or they start to speak.
export function interrupt(reason: "typing" | "interrupted" | "user" | "notice") {
  current?.stop(reason);
}

// heardUntil is the spoken cursor of the message that plays now.
export const heardUntil = () => current?.heard ?? 0;

// unlocked is true when the page can play audio (after a click or a key).
export const unlocked = () => ctx !== null && ctx.state === "running";

// chime plays a short two-tone signal before the computer speaks on its
// own (a notice). It does not go through the ducked output.
export function chime(high = false) {
  if (!ctx) return;
  const t0 = ctx.currentTime + 0.02;
  const tones = high ? [1175, 1568, 1175, 1568] : [1319, 1760];
  tones.forEach((f, i) => {
    const o = ctx!.createOscillator(), g = ctx!.createGain();
    o.type = "sine";
    o.frequency.value = f;
    const t = t0 + i * 0.09;
    g.gain.setValueAtTime(0, t);
    g.gain.linearRampToValueAtTime(0.18, t + 0.01);
    g.gain.exponentialRampToValueAtTime(0.001, t + 0.08);
    o.connect(g).connect(ctx!.destination);
    o.start(t);
    o.stop(t + 0.09);
  });
  return new Promise<void>((ok) => setTimeout(ok, (tones.length * 90 + 120)));
}

// ack plays a short rising tone when Magi accepts a message and starts to
// build the reply, so that the person knows it heard them. It differs from
// the notice chime (two tones): one glide, softer.
export function ack() {
  if (!ctx || ctx.state !== "running") return;
  const t = ctx.currentTime + 0.01;
  const o = ctx.createOscillator(), g = ctx.createGain();
  o.type = "sine";
  o.frequency.setValueAtTime(880, t);
  o.frequency.exponentialRampToValueAtTime(1320, t + 0.07);
  g.gain.setValueAtTime(0, t);
  g.gain.linearRampToValueAtTime(0.12, t + 0.008);
  g.gain.exponentialRampToValueAtTime(0.001, t + 0.11);
  o.connect(g).connect(ctx.destination);
  o.start(t);
  o.stop(t + 0.12);
}
