package community.catalyst.magi

import kotlin.math.log10
import kotlin.math.min

/**
 * Voice activity detection on the energy of short frames, with a noise
 * floor that follows the room. The same detector as the web client
 * (magi/web/src/vad.ts); keep the two the same.
 *
 * A caller can add a check of each loud frame (a speech model): then a
 * loud noise is not speech.
 *
 * Events: ONSET (speech for onsetMs: the person may take the turn), TURN
 * (speech for turnMs), END (silence for endMs after speech of at least
 * minSpeechMs), CANCEL (speech that stopped before minSpeechMs).
 */
class Vad(
    private val frameMs: Int = 20,
    private val marginDb: Double = 12.0,
    private val minDb: Double = -50.0,
    private val onsetMs: Int = 120,
    val turnMs: Int = 700,
    private val endMs: Int = 800,
    private val minSpeechMs: Int = 300,
) {
    enum class Event { ONSET, TURN, END, CANCEL }

    var floor = -60.0
        private set
    var speechMs = 0
        private set
    private var silenceMs = 0
    var active = false
        private set
    private var turned = false
    /** More margin, for example while the persona speaks. */
    var extraDb = 0.0
    private var seenMs = 0
    private val calibrateMs = 300

    /** The last frame was loud enough to be speech (before the check of speech). */
    var loud = false
        private set
    /** The last frame was speech. */
    var voiced = false
        private set

    /**
     * check: a second opinion on a loud frame, for example a speech model.
     * It runs only for a loud frame, so a quiet room costs no model time.
     */
    fun push(frame: ShortArray, check: ((ShortArray) -> Boolean)? = null): Event? {
        val db = db(frame)
        if (seenMs < calibrateMs) {
            floor = if (seenMs == 0) db else min(floor, db)
            seenMs += frameMs
            return null
        }
        loud = db > floor + marginDb + extraDb && db > minDb
        val speech = loud && (check == null || check(frame))
        voiced = speech
        floor += (db - floor) * when {
            speech -> 0.003
            db < floor -> 0.2
            else -> 0.02
        }
        if (speech) {
            speechMs += frameMs
            silenceMs = 0
        } else {
            silenceMs += frameMs
            if (!active && silenceMs >= onsetMs) speechMs = 0
        }
        if (!active) {
            if (speech && speechMs >= onsetMs) {
                active = true
                turned = false
                return Event.ONSET
            }
            return null
        }
        if (!turned && speechMs >= turnMs) {
            turned = true
            return Event.TURN
        }
        if (silenceMs >= endMs) {
            val long = speechMs >= minSpeechMs
            active = false
            speechMs = 0
            return if (long) Event.END else Event.CANCEL
        }
        return null
    }

    companion object {
        fun db(frame: ShortArray): Double {
            var sum = 0.0
            for (s in frame) {
                val x = s / 32768.0
                sum += x * x
            }
            return 10 * log10(sum / maxOf(1, frame.size) + 1e-12)
        }

        /** The samples of the frames as numbers from -1 to 1, as the models take them. */
        fun floats(frames: List<ShortArray>, from: Int = 0): FloatArray {
            var n = 0
            for (i in from until frames.size) n += frames[i].size
            val out = FloatArray(n)
            var o = 0
            for (i in from until frames.size) for (s in frames[i]) out[o++] = s / 32768f
            return out
        }

        /**
         * The samples of a 16-bit WAV file at 16 kHz, one channel (the mean of
         * the channels; linear interpolation from another rate), or null for
         * another format. Good enough for a voice print of played speech.
         */
        fun samples(wav: ByteArray): FloatArray? {
            if (wav.size < 44 || String(wav, 0, 4, Charsets.US_ASCII) != "RIFF" || String(wav, 8, 4, Charsets.US_ASCII) != "WAVE") return null
            val b = java.nio.ByteBuffer.wrap(wav).order(java.nio.ByteOrder.LITTLE_ENDIAN)
            var at = 12
            var channels = 0
            var rate = 0
            var bits = 0
            while (at + 8 <= wav.size) {
                val id = String(wav, at, 4, Charsets.US_ASCII)
                val size = b.getInt(at + 4)
                val body = at + 8
                if (id == "fmt " && size >= 16) {
                    if (b.getShort(body).toInt() != 1) return null
                    channels = b.getShort(body + 2).toInt()
                    rate = b.getInt(body + 4)
                    bits = b.getShort(body + 14).toInt()
                } else if (id == "data") {
                    if (channels < 1 || rate < 8000 || bits != 16) return null
                    // A stream has the size 0 or -1: the data go to the end.
                    val end = if (size <= 0 || body + size > wav.size) wav.size else body + size
                    val n = (end - body) / (2 * channels)
                    val mono = FloatArray(n) { i ->
                        var sum = 0f
                        for (c in 0 until channels) sum += b.getShort(body + (i * channels + c) * 2) / 32768f
                        sum / channels
                    }
                    if (rate == 16_000) return mono
                    val out = FloatArray((n.toLong() * 16_000 / rate).toInt())
                    for (i in out.indices) {
                        val x = i.toDouble() * rate / 16_000
                        val j = x.toInt()
                        val f = (x - j).toFloat()
                        out[i] = if (j + 1 < n) mono[j] * (1 - f) + mono[j + 1] * f else mono[minOf(j, n - 1)]
                    }
                    return out
                }
                if (size < 0) return null
                at = body + size + (size and 1)
            }
            return null
        }

        /** A 16-bit mono WAV file of the frames at rate. */
        fun wav(frames: List<ShortArray>, rate: Int): ByteArray {
            val n = frames.sumOf { it.size }
            val out = java.nio.ByteBuffer.allocate(44 + n * 2).order(java.nio.ByteOrder.LITTLE_ENDIAN)
            out.put("RIFF".toByteArray()).putInt(36 + n * 2).put("WAVEfmt ".toByteArray())
            out.putInt(16).putShort(1).putShort(1).putInt(rate).putInt(rate * 2).putShort(2).putShort(16)
            out.put("data".toByteArray()).putInt(n * 2)
            for (f in frames) for (s in f) out.putShort(s)
            return out.array()
        }
    }
}
