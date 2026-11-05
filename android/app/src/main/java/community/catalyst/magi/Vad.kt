package community.catalyst.magi

import kotlin.math.log10
import kotlin.math.min

/**
 * Voice activity detection on the energy of short frames, with a noise
 * floor that follows the room. The same detector as the web client
 * (magi/web/src/vad.ts); keep the two the same.
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

    fun push(frame: ShortArray): Event? {
        val db = db(frame)
        if (seenMs < calibrateMs) {
            floor = if (seenMs == 0) db else min(floor, db)
            seenMs += frameMs
            return null
        }
        val speech = db > floor + marginDb + extraDb && db > minDb
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
