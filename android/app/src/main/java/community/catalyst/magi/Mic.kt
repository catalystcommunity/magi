package community.catalyst.magi

import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.ApplicationInfo
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.NoiseSuppressor
import android.util.Log
import java.io.File

/**
 * The sound that the app hears: 16 kHz, one channel, in frames. It is the
 * microphone of the phone.
 *
 * A debug build has a second source for tests with no person and no
 * microphone (an emulator): when the directory `mic-test` is in the files
 * of the app, the sound is the WAV files that a tester puts there, one
 * after the other at the speed of speech, and silence between them. Only
 * USB debugging can make that directory (android/README.md, "Tests").
 */
abstract class Mic {
    /** Fills the frame. It returns false when the sound stopped. */
    abstract fun read(frame: ShortArray): Boolean
    abstract fun close()

    companion object {
        const val RATE = 16_000

        /**
         * Opens the sound. source is a MediaRecorder.AudioSource; cancelEcho
         * turns the echo canceller and the noise suppressor of the phone on.
         * It returns null when the phone refuses the microphone.
         */
        fun open(context: Context, source: Int, cancelEcho: Boolean): Mic? {
            if (context.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0) {
                val dir = File(context.filesDir, "mic-test")
                if (dir.isDirectory) return Files(dir)
            }
            return Phone.open(source, cancelEcho)
        }
    }

    private class Phone(private val rec: AudioRecord) : Mic() {
        override fun read(frame: ShortArray): Boolean {
            var got = 0
            while (got < frame.size) {
                val n = rec.read(frame, got, frame.size - got)
                if (n <= 0) return false
                got += n
            }
            return true
        }

        override fun close() {
            runCatching { rec.stop() }
            rec.release()
        }

        companion object {
            @SuppressLint("MissingPermission") // the callers check RECORD_AUDIO
            fun open(source: Int, cancelEcho: Boolean): Mic? = try {
                val min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
                // A buffer of one second: a model that takes long loses no audio.
                val rec = AudioRecord(source, RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, maxOf(min, RATE * 2))
                if (rec.state != AudioRecord.STATE_INITIALIZED) {
                    rec.release()
                    null
                } else {
                    if (cancelEcho) {
                        if (AcousticEchoCanceler.isAvailable()) AcousticEchoCanceler.create(rec.audioSessionId)?.enabled = true
                        if (NoiseSuppressor.isAvailable()) NoiseSuppressor.create(rec.audioSessionId)?.enabled = true
                    }
                    rec.startRecording()
                    if (rec.recordingState != AudioRecord.RECORDSTATE_RECORDING) {
                        rec.release()
                        null
                    } else Phone(rec)
                }
            } catch (e: Exception) {
                Log.w("MagiMic", "microphone: $e")
                null
            }
        }
    }

    /** The test source: the WAV files of a directory, in the order of their names. */
    private class Files(private val dir: File) : Mic() {
        private var samples = FloatArray(0)
        private var at = 0
        private var next = System.nanoTime()
        private var seed = 1

        override fun read(frame: ShortArray): Boolean {
            // The speed of a microphone.
            next += frame.size * 1_000_000_000L / RATE
            val wait = (next - System.nanoTime()) / 1_000_000
            if (wait > 0) Thread.sleep(wait) else if (wait < -500) next = System.nanoTime()
            if (at >= samples.size) {
                samples = FloatArray(0)
                at = 0
                dir.listFiles { f -> f.name.endsWith(".wav") }?.minByOrNull { it.name }?.let { f ->
                    samples = runCatching { Vad.samples(f.readBytes()) }.getOrNull() ?: FloatArray(0)
                    f.delete()
                    Log.i("MagiMic", "test sound ${f.name}: ${samples.size / (RATE / 1000)} ms")
                }
            }
            for (i in frame.indices) {
                // A little noise, as a quiet room has.
                seed = seed * 1103515245 + 12345
                val noise = ((seed ushr 16) and 0x7) - 3
                frame[i] = if (at < samples.size) (samples[at++] * 32767f).toInt().coerceIn(-32768, 32767).toShort() else noise.toShort()
            }
            return true
        }

        override fun close() {}
    }
}
