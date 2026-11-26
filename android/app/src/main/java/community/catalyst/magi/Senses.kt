package community.catalyst.magi

import android.content.Context
import android.util.Log
import com.k2fsa.sherpa.onnx.FeatureConfig
import com.k2fsa.sherpa.onnx.KeywordSpotter
import com.k2fsa.sherpa.onnx.KeywordSpotterConfig
import com.k2fsa.sherpa.onnx.OnlineModelConfig
import com.k2fsa.sherpa.onnx.OnlineStream
import com.k2fsa.sherpa.onnx.OnlineTransducerModelConfig
import com.k2fsa.sherpa.onnx.SileroVadModelConfig
import com.k2fsa.sherpa.onnx.SpeakerEmbeddingExtractor
import com.k2fsa.sherpa.onnx.SpeakerEmbeddingExtractorConfig
import com.k2fsa.sherpa.onnx.VadModelConfig

/**
 * The models that run on the phone (android/README.md, "Voice models"):
 * a speech detector, a spotter for the wake names, and a speaker model for
 * the voice print. They run with sherpa-onnx on the processor of the phone.
 * No audio leaves the phone for them.
 */
class Senses private constructor(context: Context) {
    private val assets = context.assets
    private val speaker = SpeakerEmbeddingExtractor(assets, SpeakerEmbeddingExtractorConfig(model = "$DIR/speaker.onnx", numThreads = 2))
    // The detector gives the chance of speech for each window; the limits of
    // a turn are in Vad.kt.
    private val detector = com.k2fsa.sherpa.onnx.Vad(assets, VadModelConfig(
        sileroVadModelConfig = SileroVadModelConfig(model = "$DIR/vad.onnx", threshold = 0.5f, minSilenceDuration = 0.1f, minSpeechDuration = 0.1f, windowSize = WINDOW),
        sampleRate = RATE, numThreads = 1))
    private val spotter = KeywordSpotter(assets, KeywordSpotterConfig(
        featConfig = FeatureConfig(sampleRate = RATE, featureDim = 80),
        modelConfig = OnlineModelConfig(
            transducer = OnlineTransducerModelConfig(encoder = "$DIR/kws/encoder.onnx", decoder = "$DIR/kws/decoder.onnx", joiner = "$DIR/kws/joiner.onnx"),
            tokens = "$DIR/kws/tokens.txt", numThreads = 1, modelType = "zipformer2"),
        keywordsFile = "$DIR/kws/keywords.txt", keywordsScore = BOOST, keywordsThreshold = THRESHOLD))
    private val unigram = Unigram.read(assets.open("$DIR/kws/bpe.model").use { it.readBytes() })
    @Volatile private var keywords = ""
    private val window = FloatArray(WINDOW)
    private var filled = 0
    private var speech = false

    /**
     * Sets the wake names: the names of the personas. A name with a sign
     * that the model does not have is no wake name; it returns those names.
     */
    fun names(names: List<String>): List<String> {
        val bad = ArrayList<String>()
        val lines = ArrayList<String>()
        for (n in names.map { it.trim() }.filter { it.isNotEmpty() }.distinctBy { it.uppercase() }) {
            val k = unigram.keyword(n)
            if (k == null) bad.add(n) else lines.add("$k @${label(n)}")
        }
        keywords = lines.joinToString("/")
        return bad
    }

    /** Is the frame speech? Call it for each loud frame, in order. Recorder thread. */
    fun speech(frame: ShortArray): Boolean {
        var i = 0
        while (i < frame.size) {
            val n = minOf(frame.size - i, WINDOW - filled)
            for (j in 0 until n) window[filled + j] = frame[i + j] / 32768f
            filled += n
            i += n
            if (filled == WINDOW) {
                speech = detector.compute(window) >= 0.5f
                filled = 0
            }
        }
        return speech
    }

    /** Listens for the wake names in a stretch of sound. Recorder thread. */
    inner class Spot {
        private val stream: OnlineStream = spotter.createStream(keywords)

        /** Returns the label of the wake name that ended in this audio, or null. */
        fun feed(samples: FloatArray): String? {
            stream.acceptWaveform(samples, RATE)
            var hit: String? = null
            while (spotter.isReady(stream)) {
                spotter.decode(stream)
                val k = spotter.getResult(stream).keyword
                if (k.isNotEmpty()) {
                    if (hit == null) hit = k
                    spotter.reset(stream)
                }
            }
            return hit
        }

        fun close() = stream.release()
    }

    /** The voice print of the speech in samples (16 kHz), or null for less than half a second. Any thread. */
    @Synchronized fun print(samples: FloatArray): FloatArray? {
        if (samples.size < RATE / 2) return null
        val s = speaker.createStream()
        try {
            s.acceptWaveform(if (samples.size > MAX_PRINT) samples.copyOf(MAX_PRINT) else samples, RATE)
            s.inputFinished()
            if (!speaker.isReady(s)) return null
            return VoicePrints.unit(speaker.compute(s))
        } finally {
            s.release()
        }
    }

    companion object {
        private const val TAG = "MagiSenses"
        private const val DIR = "voice"
        const val RATE = 16_000
        private const val WINDOW = 512
        private const val MAX_PRINT = RATE * 10
        // With these values: one false wake in 2 hours of read speech of
        // other people, none in 90 minutes of the persona voices, and 31 of
        // 34 spoken names found (evals/voice). The voice print and the name
        // check of Magi come after a wake, so a false wake costs one check
        // and opens nothing.
        private const val BOOST = 2.0f
        private const val THRESHOLD = 0.15f

        /** The label of a wake name: the spotter gives it back for a hit. */
        fun label(name: String): String = name.trim().uppercase().replace(Regex("[^A-Z0-9]+"), "_")

        @Volatile private var loaded: Senses? = null
        @Volatile private var failed = false

        /** True when this build has the models. */
        fun present(context: Context): Boolean = runCatching { context.assets.list(DIR)?.contains("speaker.onnx") == true }.getOrDefault(false)

        /**
         * Loads the models once. It takes about a second: call it from a
         * worker thread. It returns null for a build with no models, and
         * when the library does not load on this phone.
         */
        @Synchronized fun load(context: Context): Senses? {
            loaded?.let { return it }
            if (failed || !present(context)) return null
            return try {
                Senses(context.applicationContext).also { loaded = it }
            } catch (e: Throwable) {
                // A missing native library is an Error, not an Exception.
                Log.w(TAG, "the voice models did not load: $e")
                failed = true
                null
            }
        }
    }
}
