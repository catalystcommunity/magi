package community.catalyst.magi

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import kotlin.math.PI
import kotlin.math.exp
import kotlin.math.ln
import kotlin.math.sin

/**
 * The acknowledgement tone: Magi accepted a message and builds the reply.
 * It is one short rising glide (880 to 1320 Hz), the same as the web
 * client, and it differs from the notice tone.
 */
object Ack {
    private const val RATE = 24_000
    private val pcm: ShortArray by lazy {
        val n = RATE * 120 / 1000
        val glide = 70.0 / 1000
        var phase = 0.0
        ShortArray(n) { i ->
            val t = i.toDouble() / RATE
            val f = if (t < glide) 880.0 * exp(ln(1320.0 / 880.0) * t / glide) else 1320.0
            phase += 2 * PI * f / RATE
            val attack = (t / 0.008).coerceAtMost(1.0)
            val decay = exp(-(t - 0.008).coerceAtLeast(0.0) / 0.016)
            (sin(phase) * attack * decay * 0.35 * Short.MAX_VALUE).toInt().toShort()
        }
    }

    /** Plays the tone; [call] uses the voice call path of hands-free talk. */
    fun play(call: Boolean) {
        runCatching {
            val usage = if (call) AudioAttributes.USAGE_VOICE_COMMUNICATION else AudioAttributes.USAGE_ASSISTANT
            val track = AudioTrack.Builder()
                .setAudioAttributes(AudioAttributes.Builder().setUsage(usage).setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION).build())
                .setAudioFormat(AudioFormat.Builder().setEncoding(AudioFormat.ENCODING_PCM_16BIT).setSampleRate(RATE).setChannelMask(AudioFormat.CHANNEL_OUT_MONO).build())
                .setTransferMode(AudioTrack.MODE_STATIC)
                .setBufferSizeInBytes(pcm.size * 2)
                .build()
            track.write(pcm, 0, pcm.size)
            track.setNotificationMarkerPosition(pcm.size)
            track.setPlaybackPositionUpdateListener(object : AudioTrack.OnPlaybackPositionUpdateListener {
                override fun onMarkerReached(t: AudioTrack) { t.release() }
                override fun onPeriodicNotification(t: AudioTrack) {}
            })
            track.play()
        }
    }
}
