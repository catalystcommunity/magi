package community.catalyst.magi

import android.content.Context
import android.media.MediaPlayer
import android.media.MediaRecorder
import android.os.Build
import java.io.File

/** Records the microphone as Ogg Opus, 16 kHz mono, which the speech service reads directly. */
class Recorder(private val context: Context) {
    private var rec: MediaRecorder? = null
    private val file = File(context.cacheDir, "say.ogg")

    val recording get() = rec != null

    fun start() {
        file.delete()
        val r = if (Build.VERSION.SDK_INT >= 31) MediaRecorder(context) else @Suppress("DEPRECATION") MediaRecorder()
        r.setAudioSource(MediaRecorder.AudioSource.VOICE_RECOGNITION)
        r.setOutputFormat(MediaRecorder.OutputFormat.OGG)
        r.setAudioEncoder(MediaRecorder.AudioEncoder.OPUS)
        r.setAudioSamplingRate(16_000)
        r.setAudioChannels(1)
        r.setAudioEncodingBitRate(24_000)
        r.setOutputFile(file.path)
        r.prepare()
        r.start()
        rec = r
    }

    /** Stops and returns the recording, or null when it is too short. */
    fun stop(): ByteArray? {
        val r = rec ?: return null
        rec = null
        return try {
            r.stop()
            file.readBytes().takeIf { it.size > 200 }
        } catch (_: RuntimeException) {
            null // stop() fails when nothing was recorded
        } finally {
            r.release()
        }
    }
}

/** Plays audio clips one after the other: one clip, or the parts of a spoken reply. */
class Player(private val context: Context) {
    private var player: MediaPlayer? = null
    private val queue = ArrayDeque<Pair<ByteArray, String>>()
    private var files = 0

    /** Plays a clip now; it stops what plays and drops the queue. */
    fun play(body: ByteArray, mediaType: String) {
        stop()
        enqueue(body, mediaType)
    }

    /** Plays a clip after the clips before it. Call it on the main thread. */
    fun enqueue(body: ByteArray, mediaType: String) {
        queue.addLast(body to mediaType)
        if (player == null) next()
    }

    val playing: Boolean get() = player != null

    private fun next() {
        val (body, mediaType) = queue.removeFirstOrNull() ?: return
        val ext = when {
            mediaType.contains("ogg") -> "ogg"
            mediaType.contains("mpeg") -> "mp3"
            else -> "wav"
        }
        val f = File(context.cacheDir, "reply-${files++ % 4}.$ext")
        f.writeBytes(body)
        player = MediaPlayer().apply {
            setDataSource(f.path)
            setOnCompletionListener {
                it.release()
                if (player === it) {
                    player = null
                    next()
                }
            }
            prepare()
            start()
        }
    }

    fun stop() {
        queue.clear()
        player?.let { runCatching { it.stop() }; it.release() }
        player = null
    }
}
