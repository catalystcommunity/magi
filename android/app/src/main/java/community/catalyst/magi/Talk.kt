package community.catalyst.magi

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioManager
import android.media.ToneGenerator
import android.os.Handler
import android.os.Looper
import android.util.Log
import community.catalyst.magi.client.magi.CheckOverlapRequest
import community.catalyst.magi.client.magi.GetAudioRequest
import community.catalyst.magi.client.magi.GetVoiceProfileRequest
import community.catalyst.magi.client.magi.ListPersonasRequest
import community.catalyst.magi.client.magi.ListenRequest
import community.catalyst.magi.client.magi.Message
import community.catalyst.magi.client.magi.Notice
import community.catalyst.magi.client.magi.PutVoiceProfileRequest
import community.catalyst.magi.client.magi.ReadRequest
import community.catalyst.magi.client.magi.ReportDeliveryRequest
import community.catalyst.magi.client.magi.SayRequest
import community.catalyst.magi.client.magi.SpeakerCheck
import community.catalyst.magi.client.magi.SpeechPart
import community.catalyst.magi.client.magi.StopSpeechRequest
import community.catalyst.magi.client.magi.VoicePrint
import java.io.File
import java.util.concurrent.Executors

/**
 * Hands-free talk for the whole app (docs/speech-interaction.md): the
 * microphone, the wake names, the conversation, and the spoken replies.
 * It belongs to the process, not to the screen: the notice service keeps
 * the process, so the person can say a name with the app in the
 * background, and the persona answers. The open screen is only a view of
 * it (View).
 *
 * The name of a persona opens a conversation. In it, a turn needs no name.
 * A closing ("Goodbye", "End of line") or five quiet minutes end it, and
 * then Magi takes no speech until the next name.
 *
 * Call it on the main thread, except where marked.
 */
object Talk {
    private const val TAG = "MagiTalk"
    /** A conversation with no turn and no reply for this time ends by itself. */
    const val CONVERSATION_IDLE_MS = 5 * 60 * 1000L
    /** After a message, the phone waits this long for the reply to speak it. */
    private const val REPLY_WAIT_MS = 3 * 60 * 1000L

    /** The open screen. */
    interface View {
        /** The microphone, the hearing, or the conversation changed. */
        fun talkChanged()
        /** A spoken turn went to this persona. */
        fun tookTurn(persona: String)
        fun talkStatus(text: String)
        fun notice(n: Notice)
        fun noticeClosed(id: String)
        /** The sign in ended. */
        fun signedOut()
    }

    private lateinit var app: Context
    lateinit var settings: Settings
        private set
    private lateinit var player: Player
    private val main = Handler(Looper.getMainLooper())
    private val work = Executors.newCachedThreadPool()
    private var rpc: HttpCarrier? = null
    private var rpcServer = ""
    private var ear: Ear? = null
    @Volatile private var senses: Senses? = null
    private var sensesAsked = false
    lateinit var prints: VoicePrints
        private set

    @Volatile var view: View? = null
        set(v) {
            field = v
            // The screen went away: speech with no name is for the open screen only.
            if (::app.isInitialized) changed()
        }

    /** The stream that the person talks in: the persona of the last turn. */
    @Volatile var persona = "persona:magi"
    /** The names of the personas, for the wake names. */
    @Volatile private var names: Map<String, String> = emptyMap()

    fun init(context: Context) {
        if (::app.isInitialized) return
        app = context.applicationContext
        settings = Settings(app)
        persona = settings.lastPersona
        player = Player(app)
        player.onIdle = { speechEnded("done") }
        prints = VoicePrints(File(app.filesDir, "voice-prints.txt"))
    }

    fun client(): HttpCarrier {
        val c = rpc
        if (c != null && rpcServer == settings.server) return c
        rpcServer = settings.server
        return HttpCarrier(settings.server) { settings.token }.also { rpc = it }
    }

    private fun background(block: () -> Unit) {
        work.execute {
            try {
                block()
            } catch (e: Exception) {
                if (e is RpcException && e.code == 401) main.post { stop(remember = false); view?.signedOut() }
                else Log.w(TAG, "call: $e")
            }
        }
    }

    private fun changed() { main.post { view?.talkChanged() } }

    // --- The microphone ------------------------------------------------------

    val on: Boolean get() = ear?.on == true
    /** The phone is in call mode on its loudspeaker (the talking state of the microphone). */
    private val inCall: Boolean get() = ear?.talking == true
    @Volatile var hearing = false
        private set
    /** Why the microphone is off though the person left it on, or "". */
    @Volatile var micProblem = ""
        private set

    /** This build has the models, so the phone finds the wake names itself. */
    val hasModels: Boolean get() = Senses.present(app)

    /** The counters of the microphone for the health report (any thread). */
    val counters: Ear.Counters? get() = ear?.counters

    /**
     * Turns the microphone on. remember: the person did it, so the app turns
     * it on again at its next start. It returns the failure, or null.
     */
    fun start(remember: Boolean = true): String? {
        if (remember) settings.micOn = true
        if (on) return null
        if (app.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            micProblem = "Magi has no permission for the microphone."
            changed()
            return micProblem
        }
        loadSenses()
        // The service gets the microphone type first: Android gives the
        // microphone to a background app only through such a service.
        Attention.start(app, mic = true)
        val e = ear ?: Ear(app, earCallbacks).also { ear = it }
        e.start()
        micProblem = ""
        changed()
        background { Attention.hello(app, client()) }
        return null
    }

    fun stop(remember: Boolean = true) {
        if (remember) settings.micOn = false
        micProblem = ""
        val e = ear ?: return
        if (e.on) {
            e.stop()
            endConversation(tone = false)
            Attention.start(app, mic = false)
            if (remember) background { Attention.hello(app, client()) }
        }
        changed()
    }

    /**
     * Turns the microphone on when the person left it on: at the start of
     * the app, and after the phone starts. Android can refuse the
     * microphone to an app that is not on the screen; then the next open of
     * the app turns it on.
     */
    fun resume() {
        if (settings.micOn && !on && settings.token != null) start(remember = false)
    }

    /** The service could not get the microphone type (Attention.kt). Any thread. */
    fun micRefused() {
        main.post {
            ear?.takeIf { it.on }?.stop()
            micProblem = "Android gives the microphone only to an open app. Open Magi, or make Magi the assistant app of the phone."
            changed()
        }
    }

    /** Stops the microphone for a moment (the voice setup records by itself). */
    fun pause() { ear?.takeIf { it.on }?.stop(); changed() }

    private fun loadSenses() {
        if (sensesAsked) return
        sensesAsked = true
        work.execute {
            val s = Senses.load(app) ?: return@execute
            s.names(names.values.toList().ifEmpty { listOf("Magi", "Archimedes", "Beatrice") })
            senses = s
            refreshNames()
            fetchProfile()
        }
    }

    /** The models, loaded when needed (the voice setup). Worker thread. */
    fun sensesNow(): Senses? = senses ?: Senses.load(app)?.also { senses = it }

    /** Reads the names of the personas: they are the wake names. Worker thread. */
    private fun refreshNames() {
        if (settings.token == null) return
        runCatching {
            val list = client().magi.listPersonas(ListPersonasRequest()).personas
            setPersonas(list.associate { it.id to it.name })
        }.onFailure { Log.w(TAG, "personas: $it") }
    }

    /** The screen read the personas. Any thread. */
    fun setPersonas(byId: Map<String, String>) {
        if (byId.isEmpty() || byId == names) return
        names = byId
        senses?.names(byId.values.toList())?.takeIf { it.isNotEmpty() }?.let { Log.w(TAG, "no wake name for $it") }
    }

    // --- Voice prints --------------------------------------------------------

    /** Gets the prints of the person from Magi when the phone has none (a new phone). Worker thread. */
    private fun fetchProfile() {
        if (prints.enrolled || settings.token == null) return
        runCatching {
            val p = client().magi.getVoiceProfile(GetVoiceProfileRequest(model = VoicePrints.MODEL))
            if (p.prints.isNotEmpty()) prints.setPrints(p.prints.map { VoicePrints.Print(VoicePrints.decode(it.vector), it.source ?: VoicePrints.ENROLL, it.createdMs.toLong()) })
        }.onFailure { Log.w(TAG, "voice profile: $it") }
        changed()
    }

    /**
     * Sends the prints of the person to Magi, so that other devices of the
     * person and the extraction of Magi have them. sample: speech of the
     * person (WAV) from the voice setup. Worker thread.
     */
    fun sendProfile(sample: ByteArray? = null) {
        client().magi.putVoiceProfile(PutVoiceProfileRequest(model = VoicePrints.MODEL,
            prints = prints.prints.map { VoicePrint(vector = VoicePrints.encode(it.vector), source = it.source, createdMs = it.createdMs.toULong()) },
            sample = sample, sampleType = sample?.let { "audio/wav" }))
    }

    private var learnedSince = 0

    // --- The conversation ----------------------------------------------------

    /** A conversation that the name of a persona opened. Its turns need no name. */
    @Volatile var conversationOpen = false
        private set
    /** The time of the last turn or the end of the last reply of the conversation. */
    private var conversationSeen = 0L
    private val idleClose = Runnable { closeIdleConversation() }

    /**
     * Magi takes speech with no name now: in a conversation, and with the
     * wake word off while the app is on the screen. With the app in the
     * background, the first turn always needs a name.
     */
    val openNow: Boolean get() = conversationOpen || (!settings.wakeWord && view != null)

    private fun openConversation() {
        val was = conversationOpen
        conversationOpen = true
        conversationSeen = System.currentTimeMillis()
        main.removeCallbacks(idleClose)
        main.postDelayed(idleClose, CONVERSATION_IDLE_MS)
        if (!was) changed()
    }

    /** The gesture turned the microphone on, and the person had left it off. */
    private var gestureMic = false

    /**
     * The assistant gesture of the phone: a conversation with no name. The
     * microphone goes off again with the conversation when the person had
     * left it off. It returns the failure, or null.
     */
    fun gesture(): String? {
        val was = on
        start(remember = false)?.let { return it }
        if (!was && !settings.micOn) gestureMic = true
        openConversation()
        Ack.play(false)
        return null
    }

    /** Magi said that the conversation ended, or nobody talked for a long time. */
    fun endConversation(tone: Boolean = true) {
        if (!conversationOpen) return
        conversationOpen = false
        main.removeCallbacks(idleClose)
        if (tone && on) runCatching { ToneGenerator(if (inCall) AudioManager.STREAM_VOICE_CALL else AudioManager.STREAM_MUSIC, 60).startTone(ToneGenerator.TONE_PROP_NACK, 200) }
        if (gestureMic) {
            gestureMic = false
            if (!settings.micOn) main.post { stop(remember = false) }
        }
        changed()
    }

    /** A microphone that nobody talks to does not stay open: the name is needed again. */
    private fun closeIdleConversation() {
        if (!conversationOpen) return
        val left = conversationSeen + CONVERSATION_IDLE_MS - System.currentTimeMillis()
        if (left > 0 || speakingId != null) main.postDelayed(idleClose, maxOf(left, 10_000L)) else endConversation()
    }

    // --- Messages ------------------------------------------------------------

    /** The last message of the person was speech: its reply is spoken. */
    @Volatile var voiceTurn = false

    /**
     * Sends a message of the person. status gets "Sending..." and then ""
     * or the failure. For speech, a reply opens the conversation and the
     * phone speaks it, also with no screen.
     */
    fun say(req: SayRequest, status: (String) -> Unit = { view?.talkStatus(it) }) {
        status("Sending...")
        work.execute {
            try {
                val r = client().magi.say(req)
                if (req.audio != null) Log.i(TAG, "spoken turn: ${if (r.ended == true) "the conversation ended" else if (r.ignored == true) "not for Magi" else "to ${r.message.persona}"}")
                main.post {
                    status("")
                    if (r.ended == true) { endConversation(); return@post }
                    if (r.ignored == true) return@post
                    val p = r.message.persona.ifEmpty { req.persona ?: persona }
                    // Magi took a spoken turn: the conversation is open, and the next turns need no name.
                    if (req.audio != null) {
                        persona = p
                        settings.lastPersona = p
                        openConversation()
                    }
                    // The tone says that Magi heard the person and builds the reply:
                    // for speech, and for text when replies are spoken.
                    if (req.audio != null || req.speakReply == true) Ack.play(inCall)
                    if (req.audio != null || req.speakReply == true) follow(p, r.message.seq)
                    // A message that names another persona ("Engineering, ...") went to that persona.
                    view?.tookTurn(p)
                }
            } catch (e: Exception) {
                if (e is RpcException && e.code == 401) main.post { status(""); stop(remember = false); view?.signedOut() }
                else main.post { status(e.message ?: e.toString()) }
            }
        }
    }

    private var followGen = 0
    private var followPersona = ""
    @Volatile private var followUntil = 0L

    /**
     * Reads the stream of a persona after a message and speaks the replies.
     * The open screen reads the stream too; a message is spoken one time
     * (speak). The read ends some minutes after the last message, or with
     * the conversation.
     */
    private fun follow(p: String, afterSeq: ULong) {
        followUntil = System.currentTimeMillis() + REPLY_WAIT_MS
        if (followPersona == p) return
        val gen = ++followGen
        followPersona = p
        work.execute {
            var after = afterSeq
            while (gen == followGen && (conversationOpen || System.currentTimeMillis() < followUntil)) {
                try {
                    val r = client().magi.read(ReadRequest(persona = p, afterSeq = after, limit = 20UL, waitMs = 25_000UL))
                    if (r.messages.isEmpty()) continue
                    after = r.messages.last().seq
                    main.post { if (gen == followGen) r.messages.forEach { arrived(it) } }
                } catch (e: Exception) {
                    if (e is RpcException && e.code == 401) break
                    Thread.sleep(3_000)
                }
            }
            main.post { if (gen == followGen) followPersona = "" }
        }
    }

    /** A new message in the stream. The screen calls it too. */
    fun arrived(m: Message) {
        if (m.authorKind != "persona" || m.modality != "voice") return
        if (voiceTurn || settings.speakReplies || on) speak(m.id, m.text)
    }

    // --- Speech --------------------------------------------------------------

    // A spoken reply comes in parts while Magi makes them (listen); each
    // part plays after the one before it.
    @Volatile var speakingId: String? = null
        private set
    private var speakingText = ""
    private var speakingDone: ((String) -> Unit)? = null
    private var speakingVoice = ""
    /** The messages that the phone spoke, so that it speaks each one time. */
    private val spoken = LinkedHashSet<String>()
    private val waiting = ArrayDeque<Pair<String, String>>()
    private var lastReplyEnd = 0L
    /** The person stopped the last reply with speech (check-overlap gave "stop"). */
    private var lastReplyStopped = false

    /**
     * Speaks a message one time: after the message that plays now. again:
     * the person asked for it (the play button); it plays now.
     */
    fun speak(id: String, text: String, again: Boolean = false, onEnd: ((String) -> Unit)? = null) {
        if (!again && onEnd == null) {
            if (!spoken.add(id)) return
            if (spoken.size > 200) spoken.iterator().let { it.next(); it.remove() }
            if (speakingId != null) { waiting.addLast(id to text); return }
        }
        if (speakingId != null) speechEnded("user")
        player.stop()
        player.volume = 1f
        // Speech plays as a voice call while the microphone is in the
        // talking state: then the phone is in call mode on its loudspeaker,
        // and its echo canceller hears the speech. In the other state the
        // call path is the earpiece, so speech plays as media.
        player.communication = inCall
        speakingId = id
        speakingText = text
        speakingDone = onEnd
        work.execute {
            var after = 0UL
            try {
                while (speakingId == id) {
                    val r = client().magi.listen(ListenRequest(message = id, after = after, waitMs = 20000UL))
                    for (p in r.parts) {
                        val a = client().magi.getAudio(GetAudioRequest(digest = p.audio))
                        main.post { if (speakingId == id) player.enqueue(a.body, a.mediaType, p) }
                        learnVoice(a.body)
                    }
                    after += r.parts.size.toULong()
                    if (r.done) {
                        if (after == 0UL) main.post { if (speakingId == id) speechEnded("error") }
                        break
                    }
                }
            } catch (e: Exception) {
                Log.w(TAG, "speak: $e")
                main.post { if (speakingId == id) { player.stop(); speechEnded("error") } }
            }
        }
    }

    /**
     * Adds what the phone plays to the print of the persona voices. With
     * it, the phone knows the voice of a persona when the microphone hears
     * it from a loudspeaker (its own, or one of another device). All
     * persona voices share one print set: the phone asks only "is this a
     * persona". Worker thread.
     */
    private fun learnVoice(wav: ByteArray) {
        val s = senses ?: return
        val voice = "persona-" + persona.removePrefix("persona:")
        if (!prints.needsVoice(voice)) return
        val samples = Vad.samples(wav) ?: return
        if (samples.size < Senses.RATE * 2) return
        s.print(samples)?.let { prints.heardVoice(voice, it) }
    }

    /** Plays one clip (a voice sample, or the audio of a message). */
    fun play(body: ByteArray, mediaType: String) {
        if (speakingId != null) speechEnded("user")
        player.communication = false
        player.play(body, mediaType)
    }

    /** The spoken cursor: the end of the last word that the person heard. */
    private fun heardUntil(): Int {
        val (tag, ms) = player.position() ?: return 0
        val part = tag as? SpeechPart ?: return 0
        var heard = part.start.toInt()
        for (w in part.words) if (w.startMs.toLong() <= ms) heard = maxOf(heard, w.end.toInt())
        return heard
    }

    /** The spoken message ended: played to the end ("done") or stopped. */
    private fun speechEnded(reason: String) {
        val id = speakingId ?: return
        lastReplyEnd = System.currentTimeMillis()
        lastReplyStopped = reason == "interrupted"
        if (conversationOpen) conversationSeen = lastReplyEnd
        val heard = if (reason == "done") speakingText.codePointCount(0, speakingText.length) else heardUntil()
        speakingId = null
        val done = speakingDone
        speakingDone = null
        player.volume = 1f
        background {
            client().magi.reportDelivery(ReportDeliveryRequest(message = id, spokenUntil = heard.toULong(), reason = reason,
                stoppedMs = if (reason == "done") null else System.currentTimeMillis().toULong()))
        }
        done?.invoke(reason)
        // The next reply, when the person did not take the turn.
        if (reason == "done" || reason == "error") waiting.removeFirstOrNull()?.let { (nextId, text) -> main.post { if (speakingId == null) speak(nextId, text, again = true) } }
        else waiting.clear()
    }

    /** Stops a spoken message when the person (or a notice) takes the turn. */
    fun interrupt(reason: String = "interrupted") {
        val id = speakingId ?: return
        speechEnded(reason)
        if (player.playing) player.stop()
        background { client().magi.stopSpeech(StopSpeechRequest(message = id, reason = reason)) }
    }

    // --- The microphone talks to Talk ----------------------------------------

    private val earCallbacks = object : Ear.Callbacks {
        override fun speaking() = speakingId
        override fun heardUntil(): Int {
            var h = 0
            val l = java.util.concurrent.CountDownLatch(1)
            main.post { h = this@Talk.heardUntil(); l.countDown() }
            l.await(200, java.util.concurrent.TimeUnit.MILLISECONDS)
            return h
        }
        override fun duck(level: Float) { main.post { player.volume = level } }
        override fun interrupt() { main.post { this@Talk.interrupt() } }
        override fun check(message: String, wav: ByteArray, heardUntil: Int, final: Boolean): String = try {
            client().magi.checkOverlap(CheckOverlapRequest(message = message, audio = wav, audioType = "audio/wav",
                heardUntil = heardUntil.toULong(), final = final, wakeWord = if (!openNow) true else null)).decision
        } catch (e: RpcException) {
            if (e.code == 404 || e.code == 501 || e.code == 503) "missing" else "error"
        } catch (e: Exception) { "error" }
        override fun turn(wav: ByteArray, check: VoicePrints.Check) { main.post { this@Talk.turn(wav, check) } }
        override fun hearing(on: Boolean) {
            if (hearing == on) return
            hearing = on
            changed()
        }
        override fun open() = openNow
        override fun senses() = senses
        override fun prints() = prints
        override fun learned() {
            // Some new prints at a time go to Magi: not one call for each turn.
            if (++learnedSince >= 4) { learnedSince = 0; background { sendProfile() } }
        }
        override fun stopped(why: String) {
            main.post { micProblem = why; endConversation(tone = false); Attention.start(app, mic = false); changed() }
        }
    }

    private fun turn(wav: ByteArray, check: VoicePrints.Check) {
        val now = System.currentTimeMillis()
        val verified = check.state == VoicePrints.State.VERIFIED
        // With no voice print, a turn that the microphone heard while a reply
        // played is the voice of the persona from the loudspeaker, not a turn
        // of the person: also when the reply ended with a failure of the
        // speech. (A reply that the person stopped is different: Magi found
        // the name of a persona in the speech.) The voice print knows better.
        val turnMs = (wav.size - 44).coerceAtLeast(0) / 32L // 16 kHz, 16 bit, one channel
        if (!verified && (speakingId != null || (!lastReplyStopped && now - lastReplyEnd < turnMs + 300))) return
        voiceTurn = true
        // The name of a persona opens a conversation. In it, a turn needs no
        // name. Magi ends it (a closing phrase alone), and then the next turn
        // needs the name again.
        if (conversationOpen && now - conversationSeen >= CONVERSATION_IDLE_MS) endConversation()
        if (conversationOpen) conversationSeen = now
        val needName = !openNow
        val speaker = when (check.state) {
            VoicePrints.State.VERIFIED -> SpeakerCheck(state = "verified", score = check.score.toDouble(), model = VoicePrints.MODEL, wake = check.wake)
            VoicePrints.State.UNSURE -> SpeakerCheck(state = "unsure", score = check.score.toDouble(), model = VoicePrints.MODEL, wake = check.wake)
            else -> null
        }
        say(SayRequest(persona = persona, audio = wav, audioType = "audio/wav", speakReply = true, wakeWord = if (needName) true else null, speaker = speaker))
    }

    // --- Notices -------------------------------------------------------------

    private val noticeQueue = ArrayDeque<Notice>()
    private var noticeSaying = false

    /**
     * A notice came (the notice service, any thread). It returns how Talk
     * delivered it, or null: then the service shows a system notification.
     * Talk says a notice only to a person who is there: the app is on the
     * screen, or a conversation is open.
     */
    fun notice(n: Notice): String? {
        val v = view
        val say = n.urgency != "low" && (v != null || (conversationOpen && on))
        if (v == null && !say) return null
        main.post {
            v?.notice(n)
            if (say) { noticeQueue.addLast(n); pumpNotices() }
        }
        return if (say) "spoken" else "shown"
    }

    /** Says the next notice when the turn is free: at once when it is urgent. */
    private fun pumpNotices() {
        if (noticeSaying) return
        val n = noticeQueue.firstOrNull() ?: return
        if (n.urgency != "high" && (speakingId != null || hearing)) {
            main.postDelayed(::pumpNotices, 400)
            return
        }
        noticeQueue.removeFirst()
        noticeSaying = true
        if (speakingId != null) interrupt("notice")
        val stream = if (inCall) AudioManager.STREAM_VOICE_CALL else AudioManager.STREAM_MUSIC
        runCatching { ToneGenerator(stream, 70).startTone(if (n.urgency == "high") ToneGenerator.TONE_PROP_BEEP2 else ToneGenerator.TONE_PROP_ACK, 250) }
        main.postDelayed({
            speak(n.message, n.text) { reason ->
                noticeSaying = false
                if (reason == "done") {
                    view?.noticeClosed(n.id)
                    background { Attention.ack(app, client(), n.id) }
                }
                pumpNotices()
            }
        }, 350)
    }
}
