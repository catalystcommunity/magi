package community.catalyst.magi

import android.Manifest
import android.content.pm.PackageManager
import android.media.MediaRecorder
import android.widget.TextView
import community.catalyst.magi.Ui.Kind
import community.catalyst.magi.client.magi.DeleteVoiceProfileRequest
import java.util.concurrent.atomic.AtomicBoolean

// The voice setup: the person reads some lines, and the phone keeps a voice
// print of each. With the prints, the phone takes speech only from the
// person, and it knows the voice of a persona from a loudspeaker
// (VoicePrints.kt). The prints go to Magi for the other devices of the
// person. The audio of the lines goes to Magi too: the extraction of Magi
// needs a sample of the voice to find it in a noisy place.

private const val RATE = 16_000
private const val FRAME = RATE / 50
/** A line needs this much speech for a good print. */
private const val MIN_SPEECH_MS = 1200
private const val WAIT_MS = 12_000
/** A print of a line must be this near to the lines before it: one person read them all. */
private const val SAME_VOICE = 0.40f

private fun lines(names: List<String>): List<String> {
    val asks = listOf("what is running right now?", "what is on my list today?", "what time is it?")
    val named = names.take(3).mapIndexed { i, n -> "Hey $n, ${asks[i]}" }
    return named + listOf(
        "Remind me to call the dentist tomorrow at nine.",
        "What is the weather like this afternoon?",
        "Thank you, that is all for now. Goodbye.",
    )
}

internal fun MainActivity.showVoiceSetup() {
    val state = ui.text("", 16f)
    val line = ui.text("", 22f, bold = true).apply { setPadding(0, ui.dp(12), 0, ui.dp(12)) }
    val hint = note("")
    val actions = ui.row()
    val stop = AtomicBoolean(false)
    fun describe() {
        val n = Talk.prints.prints.size
        state.text = when {
            !Talk.hasModels -> "This build of the app has no voice models. Magi finds the names in the words, and it cannot tell voices apart."
            Talk.prints.enrolled -> "Magi knows your voice ($n prints). It takes speech only from you. It adds a print from time to time, so it follows your voice."
            else -> "Magi does not know your voice. It takes speech from each person who says a name."
        }
    }
    lateinit var showActions: () -> Unit
    fun record() {
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            hint.text = "Permit the microphone first: touch the microphone button of a conversation."
            return
        }
        val wasOn = Talk.on
        Talk.pause()
        stop.set(false)
        actions.removeAllViews()
        actions.addView(ui.button("Stop", Kind.LINK) { stop.set(true) })
        val names = personaNames()
        background(hint) {
          try {
            val senses = Talk.sensesNow() ?: throw IllegalStateException("The voice models did not load on this phone.")
            val texts = lines(names)
            val prints = ArrayList<VoicePrints.Print>()
            val speech = ArrayList<ShortArray>()
            var i = 0
            var tries = 0
            while (i < texts.size && !stop.get()) {
                runOnUiThread {
                    line.text = "“${texts[i]}”"
                    hint.text = "Line ${i + 1} of ${texts.size}. Read it aloud, as you talk to Magi."
                }
                val frames = recordLine(this, stop)
                if (stop.get()) break
                val problem = when {
                    frames == null -> "I did not hear you. Read the line again."
                    else -> {
                        val v = senses.print(Vad.floats(frames))
                        val mean = VoicePrints.meanOf(prints.map { it.vector })
                        when {
                            v == null -> "That was too short. Read the line again."
                            mean != null && VoicePrints.cosine(mean, v) < SAME_VOICE -> "That did not sound like the lines before it (a noise, or another voice). Read the line again."
                            else -> {
                                prints.add(VoicePrints.Print(v, VoicePrints.ENROLL, System.currentTimeMillis()))
                                speech.addAll(frames)
                                null
                            }
                        }
                    }
                }
                if (problem == null) { i++; tries = 0 } else {
                    if (++tries >= 4) throw IllegalStateException("The room is too loud for the setup now. Do it again in a quiet place.")
                    runOnUiThread { hint.text = problem }
                    Thread.sleep(1500)
                }
            }
            if (i == texts.size) {
                Talk.prints.setPrints(prints)
                // 30 seconds of the lines are enough as the sample for the extraction.
                val sample = Vad.wav(speech.take(RATE * 30 / FRAME), RATE)
                val sent = runCatching { Talk.sendProfile(sample) }
                runOnUiThread {
                    line.text = ""
                    hint.text = if (sent.isSuccess) "Done. Your other devices get the voice print from Magi." else "Done on this phone. Magi did not take the voice print (${sent.exceptionOrNull()?.message}); the phone sends it again later."
                }
            } else runOnUiThread { line.text = ""; hint.text = "Stopped. Nothing changed." }
          } finally {
            runOnUiThread {
                line.text = ""
                if (wasOn) Talk.start(remember = false)
                describe()
                showActions()
            }
          }
        }
    }
    showActions = {
        actions.removeAllViews()
        if (Talk.hasModels) actions.addView(ui.button(if (Talk.prints.enrolled) "Read the lines again" else "Start", Kind.PRIMARY) { record() })
        if (Talk.prints.prints.isNotEmpty()) {
            actions.addView(ui.gap(8))
            actions.addView(ui.button("Forget my voice", Kind.DANGER) {
                Talk.prints.clear()
                describe()
                showActions()
                background(hint) {
                    client().magi.deleteVoiceProfile(DeleteVoiceProfileRequest())
                    runOnUiThread { hint.text = "This phone and Magi have no voice print of you now." }
                }
            })
        }
    }
    describe()
    showActions()
    page("Talk", "Your voice",
        state,
        note("You read six short lines in a quiet room. The phone makes a voice print of each line. Your voice selects what Magi hears; it gives no permission for anything. The voice prints and the audio of the lines stay on this phone and in your Magi."),
        line, hint, actions)
    // Leaving the page stops a recording.
    val to = back
    back = { stop.set(true); to?.invoke() }
}

/**
 * Records one read line: from the start of speech to a pause. It returns
 * null when nobody spoke, or the speech was too short.
 */
private fun recordLine(context: android.content.Context, stop: AtomicBoolean): List<ShortArray>? {
    // The same source as the microphone that waits for a name.
    val rec = Mic.open(context, MediaRecorder.AudioSource.VOICE_RECOGNITION, cancelEcho = false) ?: return null
    val vad = Vad(endMs = 1000)
    val preroll = ArrayDeque<ShortArray>()
    var frames: MutableList<ShortArray>? = null
    var speechMs = 0
    var waited = 0
    try {
        while (!stop.get()) {
            val f = ShortArray(FRAME)
            if (!rec.read(f)) return null
            val cur = frames
            if (cur != null) cur.add(f) else {
                preroll.addLast(f)
                if (preroll.size > 20) preroll.removeFirst()
                waited += 20
                if (waited > WAIT_MS) return null
            }
            val ev = vad.push(f)
            if (cur != null && vad.voiced) speechMs += 20
            when (ev) {
                Vad.Event.ONSET -> { frames = preroll.toMutableList(); preroll.clear() }
                Vad.Event.END -> return if (speechMs >= MIN_SPEECH_MS) cur else null
                Vad.Event.CANCEL -> { frames = null; speechMs = 0 }
                else -> if (cur != null && cur.size > 50 * 20) return if (speechMs >= MIN_SPEECH_MS) cur else null
            }
        }
        return null
    } finally {
        rec.close()
    }
}
