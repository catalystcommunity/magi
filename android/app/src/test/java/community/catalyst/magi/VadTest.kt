package community.catalyst.magi

import kotlin.math.PI
import kotlin.math.pow
import kotlin.math.sin
import kotlin.math.sqrt
import kotlin.test.Test
import kotlin.test.assertEquals

/** The same cases as the web detector (magi/web/browser_test.go, vadCases). */
class VadTest {
    private fun frame(db: Double, i: Int): ShortArray {
        val a = 10.0.pow(db / 20) * sqrt(2.0)
        return ShortArray(320) { k -> (a * sin(2 * PI * 220 * (i * 320 + k) / 16000) * 32767).toInt().toShort() }
    }

    private fun run(vararg parts: Pair<Int, Double>): String {
        val v = Vad()
        val ev = mutableListOf<String>()
        var i = 0
        for ((ms, db) in parts) {
            var t = 0
            while (t < ms) {
                v.push(frame(db, i))?.let { ev.add("${it.name.lowercase()}@${i * 20}") }
                t += 20
                i++
            }
        }
        return ev.joinToString(" ")
    }

    @Test fun speech() = assertEquals("onset@1100 turn@1680 end@3280", run(1000 to -70.0, 1500 to -20.0, 1200 to -70.0))
    @Test fun shortSound() = assertEquals("onset@1100 cancel@1980", run(1000 to -70.0, 200 to -20.0, 1200 to -70.0))
    @Test fun steadyNoise() = assertEquals("", run(3000 to -40.0))
    @Test fun pause() = assertEquals("onset@1100 turn@2080 end@3380", run(1000 to -70.0, 600 to -20.0, 400 to -70.0, 600 to -20.0, 1200 to -70.0))

    /** A check of the loud frames (a speech model): a loud noise that is not speech is no turn. */
    @Test fun noiseIsNotSpeech() {
        val v = Vad()
        val ev = mutableListOf<String>()
        var asked = 0
        for (i in 0 until 150) v.push(frame(if (i < 50) -70.0 else -20.0, i)) { asked++; false }?.let { ev.add(it.name) }
        assertEquals(emptyList(), ev)
        // The check ran only for the loud frames, and not for the 50 quiet
        // ones. A steady noise becomes the floor of the room after a second
        // or two: then it is not loud, and the check does not run.
        assertEquals(true, asked in 50..90, "asked $asked")
        assertEquals(false, v.loud)
        assertEquals(false, v.voiced)
    }

    /** The samples of a WAV file for the speaker model: one channel at 16 kHz. */
    @Test fun samples() {
        val mono = Vad.samples(Vad.wav(listOf(ShortArray(1600) { 16384 }), 16000))!!
        assertEquals(1600, mono.size)
        assertEquals(0.5f, mono[10])
        // 24 kHz (the speech of a persona) becomes 16 kHz.
        val fast = Vad.samples(Vad.wav(listOf(ShortArray(2400) { (it % 100 * 100).toShort() }), 24000))!!
        assertEquals(1600, fast.size)
        assertEquals(null, Vad.samples(ByteArray(100)))
        assertEquals(null, Vad.samples("OggS".toByteArray() + ByteArray(100)))
        assertEquals(3200, Vad.floats(listOf(ShortArray(1600), ShortArray(1600), ShortArray(1600)), from = 1).size)
    }

    @Test fun wav() {
        val w = Vad.wav(listOf(ShortArray(8000) { 1000 }), 16000)
        val b = java.nio.ByteBuffer.wrap(w).order(java.nio.ByteOrder.LITTLE_ENDIAN)
        assertEquals(44 + 16000, w.size)
        assertEquals(16000, b.getInt(24))
        assertEquals(1000.toShort(), b.getShort(44))
    }
}
