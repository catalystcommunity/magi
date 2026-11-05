package community.catalyst.magi

import android.annotation.SuppressLint
import android.content.Context
import android.media.AudioDeviceInfo
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioRecord
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.NoiseSuppressor
import android.os.Build
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Hands-free talk (docs/speech-interaction.md, 2.3.1, steps 1 and 2), the
 * same flow as the web client (magi/web/src/listen.ts). The microphone
 * stays on with the voice-call source, so the echo canceller of the phone
 * removes the persona voice. While hands-free talk is on, the phone is in
 * communication mode on its loudspeaker, and speech plays as a voice call
 * (Player.communication), so the echo canceller hears it.
 *
 *  - While a persona speaks, an onset lowers its volume. Magi decides from
 *    the words (check-overlap): "stop" stops the persona and the speech is
 *    the next turn; "continue" (a backchannel) restores the volume and the
 *    speech is not a turn. Speech of 2.5 s is a turn in any case; without
 *    check-overlap, speech of 700 ms stops the persona.
 *  - At the end of a turn, the audio (WAV, 16 kHz) goes to Magi.
 *
 * The callbacks run on the recorder thread, except where marked.
 */
class HandsFree(private val context: Context, private val cb: Callbacks) {
    interface Callbacks {
        /** The id of the persona message that plays now, or null. */
        fun speaking(): String?
        /** The spoken cursor in the text of that message. */
        fun heardUntil(): Int
        fun duck(level: Float)
        fun interrupt()
        /** Calls check-overlap; returns the decision, or "error" / "missing". Runs on a worker thread. */
        fun check(message: String, wav: ByteArray, heardUntil: Int, final: Boolean): String
        fun turn(wav: ByteArray)
        fun hearing(on: Boolean)
    }

    companion object {
        const val RATE = 16_000
        private const val FRAME = RATE / 50 // 20 ms
        private const val PREROLL_FRAMES = 20 // 400 ms
        private const val FIRST_CHECK_MS = 400
        private const val CHECK_EVERY_MS = 300
        private const val LONG_TURN_MS = 2500
        private const val MAX_TURN_FRAMES = 3000 // 60 s
    }

    private val running = AtomicBoolean(false)
    private var thread: Thread? = null
    private val checks = Executors.newSingleThreadExecutor()
    @Volatile private var overlapAvailable = true
    private val audio = context.getSystemService(AudioManager::class.java)
    private var oldMode = AudioManager.MODE_NORMAL

    val on: Boolean get() = running.get()

    private inner class Overlap(val message: String) {
        @Volatile var busy = false
        @Volatile var stopped = false
        var lastMs = 0
    }

    @SuppressLint("MissingPermission") // the activity asks for RECORD_AUDIO first
    fun start() {
        if (!running.compareAndSet(false, true)) return
        oldMode = audio.mode
        audio.mode = AudioManager.MODE_IN_COMMUNICATION
        if (Build.VERSION.SDK_INT >= 31) {
            audio.availableCommunicationDevices.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }?.let { audio.setCommunicationDevice(it) }
        } else @Suppress("DEPRECATION") { audio.isSpeakerphoneOn = true }
        val min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        val rec = AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION, RATE, AudioFormat.CHANNEL_IN_MONO,
            AudioFormat.ENCODING_PCM_16BIT, maxOf(min, FRAME * 2 * 10))
        if (AcousticEchoCanceler.isAvailable()) AcousticEchoCanceler.create(rec.audioSessionId)?.enabled = true
        if (NoiseSuppressor.isAvailable()) NoiseSuppressor.create(rec.audioSessionId)?.enabled = true
        thread = Thread({ loop(rec) }, "magi-hands-free").also { it.start() }
    }

    fun stop() {
        if (!running.compareAndSet(true, false)) return
        thread?.join(500)
        thread = null
        if (Build.VERSION.SDK_INT >= 31) audio.clearCommunicationDevice() else @Suppress("DEPRECATION") { audio.isSpeakerphoneOn = false }
        audio.mode = oldMode
    }

    private fun loop(rec: AudioRecord) {
        val vad = Vad()
        val preroll = ArrayDeque<ShortArray>()
        var frames: MutableList<ShortArray>? = null
        var ov: Overlap? = null
        var elapsed = 0
        fun takeTurn(o: Overlap) {
            if (o.stopped) return
            o.stopped = true
            cb.duck(1f)
            cb.interrupt()
        }
        fun check(o: Overlap, final: Boolean, audio: List<ShortArray>, then: ((String) -> Unit)? = null) {
            o.busy = true
            o.lastMs = elapsed
            val wav = Vad.wav(audio, RATE)
            val heard = cb.heardUntil()
            checks.execute {
                val d = cb.check(o.message, wav, heard, final)
                if (d == "missing") overlapAvailable = false
                if (d == "stop") takeTurn(o) else if (d == "continue") cb.duck(1f)
                o.busy = false
                then?.invoke(d)
            }
        }
        rec.startRecording()
        try {
            while (running.get()) {
                val f = ShortArray(FRAME)
                var got = 0
                while (got < FRAME && running.get()) {
                    val n = rec.read(f, got, FRAME - got)
                    if (n <= 0) break
                    got += n
                }
                if (got < FRAME) continue
                elapsed += 20
                val cur = frames
                if (cur != null) cur.add(f) else {
                    preroll.addLast(f)
                    if (preroll.size > PREROLL_FRAMES) preroll.removeFirst()
                }
                vad.extraDb = if (cb.speaking() != null) 6.0 else 0.0
                when (vad.push(f)) {
                    Vad.Event.ONSET -> {
                        ov = cb.speaking()?.let { Overlap(it) }
                        if (ov != null) cb.duck(0.25f)
                        frames = preroll.toMutableList()
                        preroll.clear()
                        cb.hearing(true)
                    }
                    Vad.Event.END -> {
                        val o = ov
                        val audio = frames
                        ov = null
                        frames = null
                        cb.hearing(false)
                        if (audio != null) when {
                            o == null || o.stopped -> { cb.duck(1f); cb.turn(Vad.wav(audio, RATE)) }
                            !overlapAvailable -> cb.duck(1f) // shorter than a turn: a backchannel
                            else -> check(o, true, audio) { d -> if (d == "stop") cb.turn(Vad.wav(audio, RATE)) else cb.duck(1f) }
                        }
                    }
                    Vad.Event.CANCEL -> {
                        ov = null
                        frames = null
                        cb.duck(1f)
                        cb.hearing(false)
                    }
                    else -> {}
                }
                val o = ov
                val audio = frames
                if (o != null && audio != null && !o.stopped) {
                    if (vad.speechMs >= (if (overlapAvailable) LONG_TURN_MS else vad.turnMs)) takeTurn(o)
                    else if (overlapAvailable && !o.busy && vad.speechMs >= FIRST_CHECK_MS && elapsed - o.lastMs >= CHECK_EVERY_MS) check(o, false, audio.toList())
                }
                if (audio != null && audio.size > MAX_TURN_FRAMES) {
                    cb.turn(Vad.wav(audio, RATE))
                    frames = null
                    ov = null
                    cb.hearing(false)
                }
            }
        } finally {
            runCatching { rec.stop() }
            rec.release()
        }
    }
}
