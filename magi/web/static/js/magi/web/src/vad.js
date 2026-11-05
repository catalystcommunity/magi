// Voice activity detection on the energy of short frames, with a noise
// floor that follows the room (docs/speech-interaction.md, 2.3 and 2.3.1).
// It is simple on purpose: the browser removes the echo of the persona
// (echoCancellation), and a frame is speech when it is clearly louder than
// the room. Silero VAD can replace it later with the same events.
//
// Events, with the time of the frame in ms:
//   "onset":  speech for onsetMs (the person may take the turn: duck)
//   "turn":   speech for turnMs (the person takes the turn: stop playback)
//   "end":    silence for endMs after speech of at least minSpeechMs
//   "cancel": speech that stopped before minSpeechMs (a short noise or a
//             backchannel: restore the volume)
export const defaultVad = { frameMs: 20, marginDb: 12, minDb: -50, onsetMs: 120, turnMs: 700, endMs: 800, minSpeechMs: 300 };
export class Vad {
    o;
    floor = -60; // dBFS: the noise of the room
    speechMs = 0; // speech in the current run (it allows short pauses)
    silenceMs = 0;
    active = false; // an onset came, and no end or cancel yet
    turned = false;
    extraDb = 0; // more margin, for example while the persona speaks
    seenMs = 0; // the first calibrateMs set the floor
    calibrateMs = 300;
    constructor(o = defaultVad) {
        this.o = o;
    }
    static db(frame) {
        let sum = 0;
        for (const x of frame)
            sum += x * x;
        return 10 * Math.log10(sum / Math.max(1, frame.length) + 1e-12);
    }
    push(frame) {
        const db = Vad.db(frame);
        if (this.seenMs < this.calibrateMs) {
            // The room before the first speech: the quietest frame.
            this.floor = this.seenMs === 0 ? db : Math.min(this.floor, db);
            this.seenMs += this.o.frameMs;
            return null;
        }
        const speech = db > this.floor + this.o.marginDb + this.extraDb && db > this.o.minDb;
        if (!speech) {
            // The floor follows quiet frames fast and loud frames slowly.
            this.floor += (db - this.floor) * (db < this.floor ? 0.2 : 0.02);
        }
        else {
            // Very slowly in speech too, so that a new steady noise (a fan)
            // becomes the floor after some seconds.
            this.floor += (db - this.floor) * 0.003;
        }
        const f = this.o.frameMs;
        if (speech) {
            this.speechMs += f;
            this.silenceMs = 0;
        }
        else {
            this.silenceMs += f;
            if (!this.active && this.silenceMs >= this.o.onsetMs)
                this.speechMs = 0;
        }
        if (!this.active) {
            if (speech && this.speechMs >= this.o.onsetMs) {
                this.active = true;
                this.turned = false;
                return "onset";
            }
            return null;
        }
        if (!this.turned && this.speechMs >= this.o.turnMs) {
            this.turned = true;
            return "turn";
        }
        if (this.silenceMs >= this.o.endMs) {
            const long = this.speechMs >= this.o.minSpeechMs;
            this.active = false;
            this.speechMs = 0;
            return long ? "end" : "cancel";
        }
        return null;
    }
}
