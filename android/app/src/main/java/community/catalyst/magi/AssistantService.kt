package community.catalyst.magi

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.service.voice.VoiceInteractionService
import android.service.voice.VoiceInteractionSession
import android.service.voice.VoiceInteractionSessionService
import android.speech.RecognitionService
import android.speech.SpeechRecognizer

/**
 * Magi as the digital assistant app of the phone. The person selects it in
 * the settings of the phone (Apps, Default apps, Digital assistant app); an
 * app cannot take the role by itself.
 *
 * The role gives Magi two things. Android keeps this service running from
 * the start of the phone, and it lets a foreground service of the
 * assistant app use the microphone when that service starts in the
 * background. Thus the names of the personas work after the phone starts,
 * with no open of the app. The assistant gesture of the phone opens a
 * conversation (AssistantSession).
 *
 * Magi does not use the hotword hardware of the phone: Android gives it
 * only to apps of the system image. The wake names run on the processor
 * (Senses.kt).
 */
class AssistantService : VoiceInteractionService() {
    override fun onReady() {
        super.onReady()
        Talk.init(this)
        // The microphone as the person left it.
        if (Settings(this).token != null) Attention.start(this, mic = false, resume = true)
    }

    companion object {
        const val EXTRA_ASSIST = "community.catalyst.magi.ASSIST"

        /** True when the person made Magi the assistant app of the phone. */
        fun active(context: Context): Boolean =
            runCatching { isActiveService(context, ComponentName(context, AssistantService::class.java)) }.getOrDefault(false)
    }
}

class AssistantSessionService : VoiceInteractionSessionService() {
    override fun onNewSession(args: Bundle?): VoiceInteractionSession = AssistantSession(this)
}

/**
 * The assistant gesture (a long press of the power or home button, or a
 * swipe from a corner): the app opens with a conversation that needs no
 * name, as after "Hey, Magi".
 */
class AssistantSession(context: Context) : VoiceInteractionSession(context) {
    override fun onShow(args: Bundle?, showFlags: Int) {
        super.onShow(args, showFlags)
        runCatching {
            context.startActivity(Intent(context, MainActivity::class.java).putExtra(AssistantService.EXTRA_ASSIST, true)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP))
        }
        hide()
    }
}

/**
 * Android wants a speech recognizer from an assistant app. Magi has no
 * recognizer for other apps: speech goes to Magi through Talk. A request
 * of another app gets an error.
 */
class AssistantRecognitionService : RecognitionService() {
    override fun onStartListening(intent: Intent?, listener: Callback?) {
        runCatching { listener?.error(SpeechRecognizer.ERROR_CLIENT) }
    }
    override fun onCancel(listener: Callback?) {}
    override fun onStopListening(listener: Callback?) {}
}
