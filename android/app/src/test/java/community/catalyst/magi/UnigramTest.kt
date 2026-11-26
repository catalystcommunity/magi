package community.catalyst.magi

import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The pieces of names against SentencePiece (unigram-cases.tsv), with the
 * piece model of the wake-name spotter from the voice directory of the build.
 */
class UnigramTest {
    private val model = Unigram.read(File(System.getProperty("magi.voice"), "assets/voice/kws/bpe.model").readBytes())

    @Test fun asSentencePiece() {
        val cases = javaClass.getResource("/unigram-cases.tsv")!!.readText().lines().filter { it.isNotBlank() && !it.startsWith("#") }
        assertTrue(cases.size >= 30)
        for (line in cases) {
            val (name, pieces) = line.split('\t')
            assertEquals(pieces, model.keyword(name), name)
        }
    }

    @Test fun signs() {
        // A hyphen divides words, and the case does not matter.
        assertEquals(model.keyword("Mary Anne"), model.keyword("mary-anne"))
        // The model has no digits and no letters with accents: no wake name.
        assertNull(model.keyword("R2D2"))
        assertNull(model.keyword("Émile"))
        assertNull(model.keyword("  "))
    }
}
