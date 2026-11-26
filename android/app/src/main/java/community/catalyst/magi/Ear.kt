package community.catalyst.magi

import android.content.Context
import android.media.AudioDeviceInfo
import android.media.AudioManager
import android.media.MediaRecorder
import android.os.Build
import android.util.Log
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/**
 * The microphone of hands-free talk (docs/speech-interaction.md, 2.3.1 and
 * 4). It has two states.
 *
 *  - Waiting for a name. The source is the plain speech source, and the
 *    phone is not in call mode, so other apps sound as usual. A quiet room
 *    costs only the loudness of each frame. A loud stretch goes to the
 *    speech detector and to the spotter of the wake names. Speech with no
 *    name is dropped on the phone. After a name, the voice print says if
 *    the person spoke; only then the turn goes to Magi.
 *  - Talking (a conversation is open, or a persona speaks). The source is
 *    the voice-call source on the loudspeaker, so the echo canceller of the
 *    phone removes the persona voice. Each turn is checked against the
 *    voice print: the voice of another person and the voice of a persona
 *    from a loudspeaker are dropped. Speech over a persona makes the
 *    persona quieter, and Magi decides from the words if the persona stops
 *    (check-overlap): "stop" stops it and the speech is the next turn;
 *    "continue" (a backchannel) restores the volume. Speech of 2.5 s is a
 *    turn in any case.
 *
 * With no models in the build, the phone cannot find a name: each turn
 * goes to Magi, which finds the name in the words. With no voice prints of
 * the person, the phone cannot tell who speaks, and each turn passes.
 *
 * The callbacks run on the recorder thread or a worker thread.
 */
class Ear(private val context: Context, private val cb: Callbacks) {
    interface Callbacks {
        /** The id of the persona message that plays now, or null. */
        fun speaking(): String?
        /** The spoken cursor in the text of that message. */
        fun heardUntil(): Int
        fun duck(level: Float)
        fun interrupt()
        /** Calls check-overlap; returns the decision, or "error" / "missing". Runs on a worker thread. */
        fun check(message: String, wav: ByteArray, heardUntil: Int, final: Boolean): String
        /** A turn that passed the checks of the phone. named: a wake name started it. */
        fun turn(wav: ByteArray, check: VoicePrints.Check)
        fun hearing(on: Boolean)
        /** True while Magi takes speech with no name: a conversation is open. */
        fun open(): Boolean
        /** The models of the phone when they are loaded, else null. */
        fun senses(): Senses?
        fun prints(): VoicePrints
        /** The prints of the person changed (a turn was kept). */
        fun learned()
        /** The microphone stopped by itself. */
        fun stopped(why: String)
    }

    /** What the microphone did since the last report, for the health of the device. */
    class Counters {
        val listenMs = AtomicLong()
        val soundMs = AtomicLong()
        val wakes = AtomicLong()
        val turns = AtomicLong()
        val dropped = AtomicLong()
    }

    companion object {
        private const val TAG = "MagiEar"
        const val RATE = Mic.RATE
        private const val FRAME = RATE / 50 // 20 ms
        private const val PREROLL_FRAMES = 20 // 400 ms
        private const val FIRST_CHECK_MS = 400
        private const val CHECK_EVERY_MS = 300
        private const val LONG_TURN_MS = 2500
        private const val MAX_TURN_FRAMES = 3000 // 60 s
        /** Sound with no name that the phone keeps while it waits for a name: the start of a wake phrase. */
        private const val WAIT_KEEP_FRAMES = 150 // 3 s
        /** Speech over a persona that the voice print looks at before the persona gets quieter. */
        private val GATE_AT_MS = intArrayOf(500, 1200, 2000)
        private const val GATE_AUDIO = RATE * 3 / 2
    }

    val counters = Counters()
    private val running = AtomicBoolean(false)
    private var thread: Thread? = null
    private val checks = Executors.newSingleThreadExecutor()
    @Volatile private var overlapAvailable = true
    private val audio = context.getSystemService(AudioManager::class.java)
    private var oldMode = AudioManager.MODE_NORMAL
    private var inCall = false

    val on: Boolean get() = running.get()

    /** The microphone is in the talking state: speech plays as a voice call. */
    @Volatile var talking = false
        private set

    private class Overlap(val message: String) {
        @Volatile var busy = false
        @Volatile var stopped = false
        var lastMs = 0
        /** 0: the voice print did not look yet. 1: the person. -1: not the person. */
        @Volatile var gate = 0
        @Volatile var gateBusy = false
        var gateStep = 0
    }

    private class Segment(val frames: ArrayDeque<ShortArray>, val talk: Boolean) {
        var overlap: Overlap? = null
        var spot: Senses.Spot? = null
        var wake: String? = null
        var speechMs = 0
    }

    fun start() {
        if (!running.compareAndSet(false, true)) return
        thread = Thread({ loop() }, "magi-ear").also { it.start() }
    }

    fun stop() {
        if (!running.compareAndSet(true, false)) return
        thread?.join(700)
        thread = null
    }

    /** Opens the microphone for a state. Returns null when the phone refuses. */
    private fun record(talk: Boolean): Mic? = try {
        call(talk)
        val source = if (talk) MediaRecorder.AudioSource.VOICE_COMMUNICATION else MediaRecorder.AudioSource.VOICE_RECOGNITION
        Mic.open(context, source, cancelEcho = talk)?.also { talking = talk }
    } catch (e: Exception) {
        Log.w(TAG, "microphone: $e")
        null
    }

    /** Call mode on the loudspeaker while the phone talks; the mode from before while it waits. */
    private fun call(on: Boolean) {
        if (on == inCall) return
        if (on) {
            oldMode = audio.mode
            audio.mode = AudioManager.MODE_IN_COMMUNICATION
            if (Build.VERSION.SDK_INT >= 31) {
                audio.availableCommunicationDevices.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }?.let { audio.setCommunicationDevice(it) }
            } else @Suppress("DEPRECATION") { audio.isSpeakerphoneOn = true }
        } else {
            if (Build.VERSION.SDK_INT >= 31) audio.clearCommunicationDevice() else @Suppress("DEPRECATION") { audio.isSpeakerphoneOn = false }
            audio.mode = oldMode
        }
        inCall = on
    }

    private fun loop() {
        var talk = cb.open() || cb.speaking() != null
        var rec = record(talk)
        if (rec == null) {
            running.set(false)
            runCatching { call(false) }
            cb.stopped("The phone did not open the microphone.")
            return
        }
        val vad = Vad()
        val preroll = ArrayDeque<ShortArray>()
        var seg: Segment? = null
        var elapsed = 0
        var why: String? = null
        try {
            while (running.get()) {
                val r = rec ?: break
                val f = ShortArray(FRAME)
                if (!r.read(f)) {
                    if (running.get()) why = "The microphone stopped."
                    break
                }
                elapsed += 20
                counters.listenMs.addAndGet(20)
                val speaking = cb.speaking()
                // The state changes between stretches of sound, so no speech is cut.
                if (seg == null && (cb.open() || speaking != null) != talk) {
                    talk = !talk
                    r.close()
                    preroll.clear()
                    rec = record(talk)
                    if (rec == null) why = "The phone did not open the microphone."
                    Log.i(TAG, if (talk) "state: talking" else "state: waiting for a name")
                    continue
                }
                val senses = cb.senses()
                val cur = seg
                if (cur != null) cur.frames.addLast(f) else {
                    preroll.addLast(f)
                    if (preroll.size > PREROLL_FRAMES) preroll.removeFirst()
                }
                vad.extraDb = if (speaking != null) 6.0 else 0.0
                val ev = if (senses != null) vad.push(f) { senses.speech(it) } else vad.push(f)
                if (vad.loud) counters.soundMs.addAndGet(20)
                if (cur != null && vad.voiced) cur.speechMs += 20
                when (ev) {
                    Vad.Event.ONSET -> {
                        val s = Segment(ArrayDeque(preroll), talk)
                        preroll.clear()
                        seg = s
                        when {
                            talk -> {
                                val o = speaking?.let { Overlap(it) }
                                s.overlap = o
                                // With prints of the person, the voice print looks first:
                                // the persona does not get quieter for its own echo.
                                if (o != null && !(senses != null && cb.prints().enrolled)) { o.gate = 1; cb.duck(0.25f) }
                                cb.hearing(true)
                            }
                            senses != null -> {
                                val spot = senses.Spot()
                                s.spot = spot
                                s.wake = spot.feed(Vad.floats(s.frames))
                                if (s.wake != null) cb.hearing(true)
                            }
                            // No models: Magi finds the name in the words of each turn.
                            else -> cb.hearing(true)
                        }
                    }
                    Vad.Event.END, Vad.Event.CANCEL -> {
                        seg = null
                        if (cur != null) finish(cur, ev == Vad.Event.END, senses)
                    }
                    else -> if (cur != null) {
                        val spot = cur.spot
                        if (spot != null && cur.wake == null) {
                            cur.wake = spot.feed(Vad.floats(listOf(f)))
                            if (cur.wake != null) cb.hearing(true)
                            // With no name yet, the phone keeps only the last seconds.
                            else while (cur.frames.size > WAIT_KEEP_FRAMES) cur.frames.removeFirst()
                        }
                        val o = cur.overlap
                        if (o != null && !o.stopped) overlap(cur, o, vad, elapsed, senses)
                        if (cur.frames.size > MAX_TURN_FRAMES) {
                            seg = null
                            finish(cur, true, senses)
                        }
                    }
                }
            }
        } catch (e: Throwable) {
            Log.w(TAG, "loop: $e")
            why = "The microphone stopped: ${e.message}"
        } finally {
            seg?.spot?.close()
            rec?.close()
            runCatching { call(false) }
            talking = false
            cb.hearing(false)
            if (why != null && running.compareAndSet(true, false)) cb.stopped(why)
        }
    }

    private fun takeTurn(o: Overlap) {
        if (o.stopped) return
        o.stopped = true
        cb.duck(1f)
        cb.interrupt()
    }

    private fun askMagi(o: Overlap, final: Boolean, frames: List<ShortArray>, elapsed: Int, then: ((String) -> Unit)? = null) {
        o.busy = true
        o.lastMs = elapsed
        val wav = Vad.wav(frames, RATE)
        val heard = cb.heardUntil()
        checks.execute {
            val d = cb.check(o.message, wav, heard, final)
            if (d == "missing") overlapAvailable = false
            if (d == "stop") takeTurn(o) else if (d == "continue") cb.duck(1f)
            o.busy = false
            then?.invoke(d)
        }
    }

    /** Speech while a persona speaks: the voice print first, then the words. */
    private fun overlap(s: Segment, o: Overlap, vad: Vad, elapsed: Int, senses: Senses?) {
        if (o.gate <= 0) {
            // Not the person at the last look, or no look yet. The print
            // looks again when there is more speech: the voice of the person
            // can come after a noise.
            if (senses == null || o.gateBusy || o.gateStep >= GATE_AT_MS.size || s.speechMs < GATE_AT_MS[o.gateStep]) return
            o.gateStep++
            o.gateBusy = true
            val all = Vad.floats(s.frames)
            val clip = if (all.size > GATE_AUDIO) all.copyOfRange(all.size - GATE_AUDIO, all.size) else all
            val speechMs = minOf(s.speechMs, GATE_AUDIO / 16)
            checks.execute {
                val c = senses.print(clip)?.let { cb.prints().check(it, speechMs, true) }
                Log.i(TAG, "speech over the persona: ${c?.state} score=${"%.2f".format(c?.score ?: 0f)}")
                if (c == null || c.state == VoicePrints.State.VERIFIED || c.state == VoicePrints.State.UNSURE || c.state == VoicePrints.State.NONE) {
                    o.gate = 1
                    if (!o.stopped) cb.duck(0.25f)
                } else o.gate = -1
                o.gateBusy = false
            }
            return
        }
        if (vad.speechMs >= (if (overlapAvailable) LONG_TURN_MS else vad.turnMs)) takeTurn(o)
        else if (overlapAvailable && !o.busy && vad.speechMs >= FIRST_CHECK_MS && elapsed - o.lastMs >= CHECK_EVERY_MS) askMagi(o, false, s.frames.toList(), elapsed)
    }

    /** A stretch of sound ended. speech: it was long enough to be a turn. */
    private fun finish(s: Segment, speech: Boolean, senses: Senses?) {
        s.spot?.close()
        s.spot = null
        val o = s.overlap
        if (s.talk || s.wake != null || senses == null) cb.hearing(false)
        if (!speech) {
            if (o != null) cb.duck(1f)
            return
        }
        if (!s.talk && senses != null && s.wake == null) return // talk in the room with no name: nothing is kept
        val frames = s.frames.toList()
        val prints = cb.prints()
        checks.execute {
            var check = VoicePrints.Check(VoicePrints.State.NONE, 0f, s.wake)
            if (senses != null && prints.enrolled) {
                val v = senses.print(Vad.floats(frames))
                if (v != null) {
                    check = prints.check(v, s.speechMs, s.talk, s.wake)
                    if (check.state == VoicePrints.State.VERIFIED && prints.learn(v, check.score, s.speechMs, System.currentTimeMillis())) cb.learned()
                }
            }
            // The log has the decision and no audio: it shows why the phone
            // took or dropped a turn.
            Log.i(TAG, "turn: ${if (s.talk) "talking" else "waiting"} wake=${s.wake} speech=${s.speechMs}ms ${check.state} score=${"%.2f".format(check.score)}")
            if (!check.pass) {
                // Another voice, or a persona from a loudspeaker.
                counters.dropped.incrementAndGet()
                if (o != null && !o.stopped) cb.duck(1f)
                return@execute
            }
            if (s.wake != null) counters.wakes.incrementAndGet()
            val wav = Vad.wav(frames, RATE)
            when {
                o == null || o.stopped -> { cb.duck(1f); counters.turns.incrementAndGet(); cb.turn(wav, check) }
                !overlapAvailable -> cb.duck(1f) // shorter than a turn: a backchannel
                else -> {
                    val d = cb.check(o.message, wav, cb.heardUntil(), true)
                    if (d == "missing") overlapAvailable = false
                    if (d == "stop") { takeTurn(o); counters.turns.incrementAndGet(); cb.turn(wav, check) } else cb.duck(1f)
                }
            }
        }
    }
}
