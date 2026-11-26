package community.catalyst.magi

import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.Base64
import kotlin.math.sqrt

/**
 * The voice prints on the phone (docs/speech-interaction.md, 3.2 and 4.2):
 * the prints of the person, and one print for each persona voice that the
 * phone played. A print is a vector of unit length from the speaker model;
 * the score of two prints is their cosine.
 *
 * The voice of the person selects what Magi hears. It gives no permission:
 * the identity is the sign in of the phone.
 */
class VoicePrints(private val file: File?) {
    class Print(val vector: FloatArray, val source: String, val createdMs: Long)

    enum class State {
        /** The phone has no prints of the person: it cannot tell who speaks. */
        NONE,
        /** The voice of the person. */
        VERIFIED,
        /** Between the limits: noise, or more than one voice. Magi looks again (extraction). */
        UNSURE,
        /** Another voice. */
        REJECTED,
        /** The voice of a persona from a loudspeaker. */
        ECHO,
    }

    class Check(val state: State, val score: Float, val wake: String? = null) {
        /** Magi can take the turn. */
        val pass: Boolean get() = state == State.NONE || state == State.VERIFIED || state == State.UNSURE
    }

    @Volatile private var own: List<Print> = emptyList()
    @Volatile private var mean: FloatArray? = null
    @Volatile private var voices: Map<String, FloatArray> = emptyMap()
    private val voiceCounts = HashMap<String, Int>()

    /** The prints of the person. */
    val prints: List<Print> get() = own

    /** The person read the lines of the voice setup (or a device of the person did). */
    val enrolled: Boolean get() = own.count { it.source == ENROLL } >= MIN_ENROLL

    init { load() }

    /** The score of a print against the voice of the person, or null with no prints. */
    fun score(v: FloatArray): Float? = mean?.let { cosine(it, v) }

    /** The persona voice that is nearest to the print, with its score. */
    fun voice(v: FloatArray): Pair<String, Float>? = voices.maxByOrNull { cosine(it.value, v) }?.let { it.key to cosine(it.value, v) }

    /**
     * Who spoke. speechMs is the speech in the clip; a short clip has a low
     * score, so a short answer in an open conversation needs less.
     */
    fun check(v: FloatArray, speechMs: Int, conversation: Boolean, wake: String? = null): Check {
        val score = score(v)
        if (!enrolled || score == null) return Check(State.NONE, 0f, wake)
        val persona = voice(v)
        val state = judge(score, persona?.second, speechMs, conversation)
        return Check(state, score, wake)
    }

    /** Replaces the prints of the person (the voice setup, or the profile from Magi). */
    @Synchronized fun setPrints(list: List<Print>) {
        own = list.filter { it.vector.isNotEmpty() }.map { Print(unit(it.vector), it.source, it.createdMs) }
        mean = meanOf(own.map { it.vector })
        save()
    }

    /**
     * Keeps the print of a turn that is the person for certain, so the
     * prints follow the voice and the rooms of the person. It returns true
     * when it kept the print.
     */
    @Synchronized fun learn(v: FloatArray, score: Float, speechMs: Int, now: Long): Boolean {
        if (!enrolled || score < LEARN || speechMs < LEARN_MS) return false
        val turns = own.filter { it.source == TURN }
        // One print for each 10 minutes: prints of one sentence said again add nothing.
        if (turns.isNotEmpty() && now - turns.last().createdMs < 10 * 60_000) return false
        val kept = own.filter { it.source != TURN } + (turns + Print(unit(v), TURN, now)).takeLast(MAX_TURNS)
        own = kept
        mean = meanOf(kept.map { it.vector })
        save()
        return true
    }

    /** Adds what the phone played in a persona voice to the print of that voice. */
    @Synchronized fun heardVoice(name: String, v: FloatArray) {
        val voice = name.replace(' ', '_')
        val n = voiceCounts[voice] ?: 0
        if (n >= VOICE_CLIPS) return
        val old = voices[voice]
        val u = unit(v)
        val next = if (old == null) u else unit(FloatArray(u.size) { (old[it] * n + u[it]) / (n + 1) })
        voiceCounts[voice] = n + 1
        voices = voices + (voice to next)
        save()
    }

    /** The persona voices that need more clips for their print. */
    @Synchronized fun needsVoice(name: String): Boolean = (voiceCounts[name.replace(' ', '_')] ?: 0) < VOICE_CLIPS

    @Synchronized fun clear() {
        own = emptyList()
        mean = null
        save()
    }

    // One line for each print: the kind, the source or the voice, a number
    // (the time, or the count of clips), and the vector in base 64.
    private fun load() {
        val f = file ?: return
        if (!f.exists()) return
        val mine = ArrayList<Print>()
        val vs = HashMap<String, FloatArray>()
        runCatching {
            for (line in f.readLines()) {
                val p = line.split(' ')
                if (p.size != 4) continue
                val v = decode(Base64.getDecoder().decode(p[3]))
                when (p[0]) {
                    "own" -> mine.add(Print(v, p[1], p[2].toLong()))
                    "voice" -> { vs[p[1]] = v; voiceCounts[p[1]] = p[2].toInt() }
                }
            }
        }
        own = mine
        mean = meanOf(mine.map { it.vector })
        voices = vs
    }

    private fun save() {
        val f = file ?: return
        val b = StringBuilder()
        for (p in own) b.append("own ${p.source} ${p.createdMs} ${Base64.getEncoder().encodeToString(encode(p.vector))}\n")
        for ((k, v) in voices) b.append("voice $k ${voiceCounts[k] ?: 1} ${Base64.getEncoder().encodeToString(encode(v))}\n")
        runCatching {
            val tmp = File(f.path + ".tmp")
            tmp.writeText(b.toString())
            tmp.renameTo(f)
        }
    }

    companion object {
        /** The speaker model. Prints of another model do not compare. */
        const val MODEL = "3dspeaker-eres2net-en-voxceleb-16k"
        const val ENROLL = "enroll"
        const val TURN = "turn"
        const val MIN_ENROLL = 3

        // The limits come from the clips of one person on this phone model
        // and 26 other speakers, in a quiet room (evals/voice): the lowest
        // score of the person on 1.5 s was 0.36, the highest of another
        // speaker 0.34 (0.42 on whole sentences). Under 10 dB of babble the
        // score of the person falls to 0.2 to 0.4: that is UNSURE, and Magi
        // extracts the voice and looks again.
        const val ACCEPT = 0.40f
        const val UNSURE = 0.25f
        /** A short answer ("yes", "thank you") in an open conversation. */
        const val SHORT_MS = 1200
        /** A print that is nearer to a persona voice than this, and nearer than to the person, is the loudspeaker. */
        const val ECHO = 0.40f
        const val LEARN = 0.60f
        const val LEARN_MS = 2000
        const val MAX_TURNS = 24
        const val VOICE_CLIPS = 8

        fun judge(person: Float, persona: Float?, speechMs: Int, conversation: Boolean): State = when {
            persona != null && persona >= ECHO && persona > person -> State.ECHO
            person >= ACCEPT -> State.VERIFIED
            person >= UNSURE && conversation && speechMs < SHORT_MS -> State.VERIFIED
            person >= UNSURE -> State.UNSURE
            else -> State.REJECTED
        }

        fun cosine(a: FloatArray, b: FloatArray): Float {
            if (a.size != b.size) return 0f
            var dot = 0f
            var na = 0f
            var nb = 0f
            for (i in a.indices) { dot += a[i] * b[i]; na += a[i] * a[i]; nb += b[i] * b[i] }
            return if (na == 0f || nb == 0f) 0f else dot / sqrt(na * nb)
        }

        fun unit(v: FloatArray): FloatArray {
            var n = 0f
            for (x in v) n += x * x
            n = sqrt(n)
            return if (n == 0f) v.copyOf() else FloatArray(v.size) { v[it] / n }
        }

        fun meanOf(vs: List<FloatArray>): FloatArray? {
            if (vs.isEmpty()) return null
            val m = FloatArray(vs[0].size)
            for (v in vs) if (v.size == m.size) for (i in m.indices) m[i] += v[i]
            return unit(m)
        }

        /** The wire form of a print: little-endian 32-bit floats. */
        fun encode(v: FloatArray): ByteArray {
            val b = ByteBuffer.allocate(v.size * 4).order(ByteOrder.LITTLE_ENDIAN)
            for (x in v) b.putFloat(x)
            return b.array()
        }

        fun decode(b: ByteArray): FloatArray {
            val buf = ByteBuffer.wrap(b).order(ByteOrder.LITTLE_ENDIAN)
            return FloatArray(b.size / 4) { buf.getFloat() }
        }
    }
}
