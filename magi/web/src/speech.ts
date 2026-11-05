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

// unlock makes the audio context in a click or a key press: browsers play
// audio only after the person did something on the page.
export function unlock() {
  try {
    ctx ??= new AudioContext();
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
  constructor(public m: Message, public shownMs: number) {}

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
    source.connect(ctx!.destination);
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
    const len = Array.from(this.m.text).length;
    const spokenUntil = reason === "done" ? len : this.heard;
    magi.reportDelivery({
      message: this.m.id, spokenUntil, shownMs: this.shownMs, reason,
      stoppedMs: reason === "done" ? undefined : Date.now(),
    }).catch(() => { /* the report is optional */ });
  }
}

let current: Playback | null = null;

// speak plays the parts of a persona message. shownMs is when the client
// showed its text.
export function speak(m: Message, shownMs = Date.now()) {
  unlock();
  if (!ctx) return notify("This browser cannot play audio.", true);
  current?.stop("user");
  const p = new Playback(m, shownMs);
  current = p;
  setSpeaking(m.id);
  p.run().catch((e) => { notify("Could not play the reply: " + errText(e), true); p.finish("error"); });
}

// interrupt stops the speech when the person takes the turn: they type,
// or they start to speak.
export function interrupt(reason: "typing" | "interrupted" | "user") {
  current?.stop(reason);
}
