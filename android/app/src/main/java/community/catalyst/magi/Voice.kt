package community.catalyst.magi

import android.content.Context
import android.media.AudioAttributes
import android.media.MediaPlayer
import java.io.File

/**
 * Plays audio clips one after the other: one clip, or the parts of a spoken
 * reply. A clip can carry a tag (the speech part), so that the app knows
 * which part plays and where (the spoken cursor). Call it on the main thread.
 */
class Player(private val context: Context) {
    private var player: MediaPlayer? = null
    private var playingTag: Any? = null
    private val queue = ArrayDeque<Triple<ByteArray, String, Any?>>()
    private var files = 0

    /** Called when the last clip ended (not when stop() stops it). */
    var onIdle: (() -> Unit)? = null

    /** The volume of speech: 1, or less while the person may take the turn. */
    var volume = 1f
        set(v) {
            field = v
            player?.setVolume(v, v)
        }

    /**
     * Play as a voice call (hands-free talk): then the echo canceller of
     * the phone hears the playback and removes it from the microphone.
     */
    var communication = false

    /** Plays a clip now; it stops what plays and drops the queue. */
    fun play(body: ByteArray, mediaType: String) {
        stop()
        enqueue(body, mediaType)
    }

    /** Plays a clip after the clips before it. */
    fun enqueue(body: ByteArray, mediaType: String, tag: Any? = null) {
        queue.addLast(Triple(body, mediaType, tag))
        if (player == null) next()
    }

    val playing: Boolean get() = player != null

    /** The tag of the clip that plays and the position in it (ms). */
    fun position(): Pair<Any?, Int>? = player?.let { p -> playingTag to runCatching { p.currentPosition }.getOrDefault(0) }

    private fun next() {
        val (body, mediaType, tag) = queue.removeFirstOrNull() ?: run { onIdle?.invoke(); return }
        val ext = when {
            mediaType.contains("ogg") -> "ogg"
            mediaType.contains("mpeg") -> "mp3"
            else -> "wav"
        }
        val f = File(context.cacheDir, "reply-${files++ % 4}.$ext")
        f.writeBytes(body)
        playingTag = tag
        player = MediaPlayer().apply {
            val usage = if (communication) AudioAttributes.USAGE_VOICE_COMMUNICATION else AudioAttributes.USAGE_MEDIA
            setAudioAttributes(AudioAttributes.Builder().setUsage(usage).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
            setDataSource(f.path)
            setOnCompletionListener {
                it.release()
                if (player === it) {
                    player = null
                    playingTag = null
                    next()
                }
            }
            prepare()
            setVolume(volume, volume)
            start()
        }
    }

    fun stop() {
        queue.clear()
        player?.let { runCatching { it.stop() }; it.release() }
        player = null
        playingTag = null
    }
}
