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

    @Test fun wav() {
        val w = Vad.wav(listOf(ShortArray(8000) { 1000 }), 16000)
        val b = java.nio.ByteBuffer.wrap(w).order(java.nio.ByteOrder.LITTLE_ENDIAN)
        assertEquals(44 + 16000, w.size)
        assertEquals(16000, b.getInt(24))
        assertEquals(1000.toShort(), b.getShort(44))
    }
}
