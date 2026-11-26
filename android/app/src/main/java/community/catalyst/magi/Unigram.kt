package community.catalyst.magi

/**
 * Splits a name into the pieces of the wake-name model, as SentencePiece
 * does for a unigram model: the split with the highest sum of the piece
 * scores. The model file (bpe.model, a protocol buffer) has the pieces and
 * the scores. The phone makes the pieces itself, so a new name of a persona
 * is a wake name with no new build and no help from Magi.
 */
class Unigram(private val scores: Map<String, Float>) {
    private val longest = scores.keys.maxOfOrNull { it.length } ?: 1
    private val lowest = scores.values.minOrNull() ?: 0f

    /**
     * The pieces of text, or null when the model cannot say it: the model
     * has the letters A to Z and the apostrophe only. A hyphen, a period,
     * and a comma divide words.
     */
    fun pieces(text: String): List<String>? {
        val plain = StringBuilder()
        for (c in text.uppercase()) when {
            c in 'A'..'Z' || c == '\'' -> plain.append(c)
            c.isWhitespace() || c == '-' || c == '.' || c == ',' -> plain.append(' ')
            else -> return null
        }
        val words = plain.trim().split(Regex(" +")).filter { it.isNotEmpty() }
        if (words.isEmpty()) return null
        val s = words.joinToString("") { SPACE + it }
        // best[i]: the highest score of a split of the first i signs.
        val best = FloatArray(s.length + 1) { Float.NEGATIVE_INFINITY }
        val from = IntArray(s.length + 1) { -1 }
        best[0] = 0f
        for (end in 1..s.length) {
            for (start in maxOf(0, end - longest) until end) {
                if (best[start] == Float.NEGATIVE_INFINITY) continue
                val score = scores[s.substring(start, end)] ?: continue
                if (best[start] + score > best[end]) {
                    best[end] = best[start] + score
                    from[end] = start
                }
            }
        }
        if (from[s.length] < 0) return null
        val out = ArrayList<String>()
        var end = s.length
        while (end > 0) {
            out.add(s.substring(from[end], end))
            end = from[end]
        }
        return out.reversed()
    }

    /** One line of keywords for the spotter: the pieces with a space between them. */
    fun keyword(name: String): String? = pieces(name)?.joinToString(" ")

    companion object {
        /** The sign that SentencePiece puts at the start of a word. */
        const val SPACE = "▁"

        /**
         * Reads the pieces of a SentencePiece model: field 1 of the model is
         * a piece, with its text (field 1), its score (field 2), and its
         * type (field 3; 1 or absent is a normal piece).
         */
        fun read(model: ByteArray): Unigram {
            val scores = HashMap<String, Float>()
            var i = 0
            fun varint(): Long {
                var v = 0L
                var shift = 0
                while (true) {
                    val b = model[i++].toInt() and 0xff
                    v = v or ((b and 0x7f).toLong() shl shift)
                    if (b < 0x80) return v
                    shift += 7
                }
            }
            fun skip(wire: Int) {
                when (wire) {
                    0 -> varint()
                    1 -> i += 8
                    // The length first: it moves i.
                    2 -> { val n = varint().toInt(); i += n }
                    5 -> i += 4
                    else -> throw IllegalArgumentException("wire type $wire")
                }
            }
            while (i < model.size) {
                val key = varint().toInt()
                if (key ushr 3 != 1 || key and 7 != 2) { skip(key and 7); continue }
                val size = varint().toInt()
                val end = i + size
                var piece = ""
                var score = 0f
                var type = 1
                while (i < end) {
                    val k = varint().toInt()
                    when {
                        k == (1 shl 3 or 2) -> { val n = varint().toInt(); piece = String(model, i, n, Charsets.UTF_8); i += n }
                        k == (2 shl 3 or 5) -> {
                            score = java.lang.Float.intBitsToFloat((model[i].toInt() and 0xff) or ((model[i + 1].toInt() and 0xff) shl 8) or
                                ((model[i + 2].toInt() and 0xff) shl 16) or ((model[i + 3].toInt() and 0xff) shl 24))
                            i += 4
                        }
                        k == (3 shl 3) -> type = varint().toInt()
                        else -> skip(k and 7)
                    }
                }
                i = end
                if (type == 1 && piece.isNotEmpty()) scores[piece] = score
            }
            return Unigram(scores)
        }
    }
}
