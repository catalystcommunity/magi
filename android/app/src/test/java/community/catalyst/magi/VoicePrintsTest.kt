package community.catalyst.magi

import community.catalyst.magi.VoicePrints.State
import java.io.File
import kotlin.math.sqrt
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class VoicePrintsTest {
    /** A unit vector with the cosine c to the first axis. */
    private fun at(c: Float, axis: Int = 1) = FloatArray(4).also { it[0] = c; it[axis] = sqrt(1 - c * c) }
    private val person = floatArrayOf(1f, 0f, 0f, 0f)
    private fun enrolled(file: File? = null) = VoicePrints(file).apply {
        setPrints(List(3) { VoicePrints.Print(person, VoicePrints.ENROLL, 1000L + it) })
    }

    @Test fun judge() {
        // The limits of a sentence.
        assertEquals(State.VERIFIED, VoicePrints.judge(0.45f, null, 3000, false))
        assertEquals(State.UNSURE, VoicePrints.judge(0.30f, null, 3000, false))
        assertEquals(State.REJECTED, VoicePrints.judge(0.20f, null, 3000, false))
        // A short answer in an open conversation needs less; a wake phrase does not.
        assertEquals(State.VERIFIED, VoicePrints.judge(0.30f, null, 800, true))
        assertEquals(State.UNSURE, VoicePrints.judge(0.30f, null, 800, false))
        assertEquals(State.REJECTED, VoicePrints.judge(0.20f, null, 800, true))
        // The voice of a persona from a loudspeaker, also when it is a little like the person.
        assertEquals(State.ECHO, VoicePrints.judge(0.30f, 0.55f, 3000, true))
        assertEquals(State.ECHO, VoicePrints.judge(0.41f, 0.60f, 3000, true))
        // The person, with a persona voice in the room that is not as near.
        assertEquals(State.VERIFIED, VoicePrints.judge(0.60f, 0.45f, 3000, true))
        assertEquals(State.VERIFIED, VoicePrints.judge(0.45f, 0.30f, 3000, true))
    }

    @Test fun noPrintsPassAll() {
        val p = VoicePrints(null)
        assertFalse(p.enrolled)
        val c = p.check(at(0.1f), 3000, false, "MAGI")
        assertEquals(State.NONE, c.state)
        assertTrue(c.pass)
        assertEquals("MAGI", c.wake)
        // Two lines are not a voice setup.
        p.setPrints(List(2) { VoicePrints.Print(person, VoicePrints.ENROLL, 1L) })
        assertFalse(p.enrolled)
    }

    @Test fun check() {
        val p = enrolled()
        assertTrue(p.enrolled)
        assertEquals(State.VERIFIED, p.check(at(0.7f), 3000, false).state)
        assertEquals(State.UNSURE, p.check(at(0.3f), 3000, false).state)
        val other = p.check(at(0.1f), 3000, true)
        assertEquals(State.REJECTED, other.state)
        assertFalse(other.pass)
        // The phone played a persona voice: a clip like it is the loudspeaker.
        val persona = at(0.2f, 2)
        p.heardVoice("persona-engineering", persona)
        assertEquals(State.ECHO, p.check(persona, 3000, true).state)
        assertEquals(State.VERIFIED, p.check(at(0.7f), 3000, true).state)
    }

    @Test fun keepsAndLearns() {
        val f = File.createTempFile("prints", ".txt").also { it.delete() }
        try {
            val p = enrolled(f)
            p.heardVoice("persona magi", at(0.1f, 3))
            // A turn that is the person for certain is kept; one that is not, or a short one, is not.
            assertFalse(p.learn(at(0.5f), 0.5f, 4000, 10_000_000))
            assertFalse(p.learn(at(0.9f), 0.9f, 900, 10_000_000))
            assertTrue(p.learn(at(0.9f), 0.9f, 4000, 10_000_000))
            // Not two prints of the same minutes.
            assertFalse(p.learn(at(0.9f), 0.9f, 4000, 10_060_000))
            assertTrue(p.learn(at(0.9f, 2), 0.9f, 4000, 11_000_000))
            val again = VoicePrints(f)
            assertTrue(again.enrolled)
            assertEquals(5, again.prints.size)
            assertEquals(2, again.prints.count { it.source == VoicePrints.TURN })
            assertTrue(again.needsVoice("persona magi"))
            assertEquals(State.ECHO, again.check(at(0.1f, 3), 3000, true).state)
            // The prints of turns have a limit; the lines of the setup stay.
            for (i in 0 until 40) again.learn(at(0.9f), 0.9f, 4000, 20_000_000L + i * 700_000L)
            assertEquals(3 + VoicePrints.MAX_TURNS, again.prints.size)
            assertEquals(3, again.prints.count { it.source == VoicePrints.ENROLL })
            again.clear()
            assertFalse(VoicePrints(f).enrolled)
        } finally {
            f.delete()
        }
    }

    @Test fun wire() {
        val v = floatArrayOf(0.25f, -1f, 3.5f)
        assertEquals(12, VoicePrints.encode(v).size)
        assertTrue(v.contentEquals(VoicePrints.decode(VoicePrints.encode(v))))
        assertNull(VoicePrints.meanOf(emptyList()))
        assertEquals(0f, VoicePrints.cosine(floatArrayOf(1f), floatArrayOf(1f, 0f)))
    }
}
