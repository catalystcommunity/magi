package community.catalyst.magi

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.view.ViewGroup.LayoutParams
import android.view.WindowInsets
import android.os.Build
import android.widget.FrameLayout
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup.LayoutParams.MATCH_PARENT
import android.view.ViewGroup.LayoutParams.WRAP_CONTENT
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import community.catalyst.magi.client.identity.BeginLoginRequest
import community.catalyst.magi.client.identity.PollLoginRequest
import community.catalyst.magi.client.identity.RefreshRequest
import community.catalyst.magi.client.magi.CancelTaskRequest
import community.catalyst.magi.client.magi.DecideEgressReviewRequest
import community.catalyst.magi.client.magi.DecideRequest
import community.catalyst.magi.client.magi.EgressReview
import community.catalyst.magi.client.magi.GetAudioRequest
import community.catalyst.magi.client.magi.GetTaskDiffRequest
import community.catalyst.magi.client.magi.AnswerReviewRequest
import community.catalyst.magi.client.magi.ListEgressReviewsRequest
import community.catalyst.magi.client.magi.ListLinksRequest
import community.catalyst.magi.client.magi.ListPersonasRequest
import community.catalyst.magi.client.magi.ListReviewsRequest
import community.catalyst.magi.client.magi.ListTasksRequest
import community.catalyst.magi.client.magi.Message
import community.catalyst.magi.client.magi.MessageLink
import community.catalyst.magi.client.magi.PersonaInfo
import community.catalyst.magi.client.magi.ReadRequest
import community.catalyst.magi.client.magi.ReviewItem
import community.catalyst.magi.client.magi.SayRequest
import community.catalyst.magi.client.magi.SetVisibilityRequest
import community.catalyst.magi.client.magi.TraceRequest
import community.catalyst.magi.client.magi.UnlinkRequest
import java.text.DateFormat
import java.util.Date
import java.util.concurrent.Executors

/**
 * The Magi Android client. It uses only the public Magi API: IdentityService
 * to sign in, MagiService for the conversation, voice, reviews, and tasks.
 *
 * There are no chats: the person has one stream with each persona. The
 * librarian links messages to earlier ones; the view shows those links as
 * "about" lines. A long press on a message shows its links and who can
 * read it, for correction.
 *
 * Sign in: the browser goes to LinkKeys and then back to this app with
 * RETURN_URL and a one-time code. The app polls with its login_id and the
 * code. The code is useless without the login_id, so an app that claims the
 * same scheme cannot use it.
 */
class MainActivity : Activity() {
    companion object {
        const val RETURN_URL = "community.catalyst.magi:/signed-in"
        const val MIC_REQUEST = 1
    }

    private lateinit var settings: Settings
    private lateinit var recorder: Recorder
    private lateinit var player: Player
    private val work = Executors.newCachedThreadPool()
    private var rpc: HttpCarrier? = null

    private var persona = "persona:magi"
    private var personas: List<PersonaInfo> = emptyList()
    private val shown = HashMap<String, View>()
    @Volatile private var lastSeq = 0UL
    @Volatile private var readGen = 0
    @Volatile private var voiceTurn = false
    private lateinit var messages: LinearLayout
    private lateinit var scroll: ScrollView

    override fun onCreate(state: Bundle?) {
        super.onCreate(state)
        settings = Settings(this)
        recorder = Recorder(this)
        player = Player(this)
        Updater.schedule(this)
        if (!handleReturn(intent)) start()
    }

    override fun onResume() {
        super.onResume()
        Updater.foreground = this
        work.execute { runCatching { Updater.check(applicationContext) } }
    }

    override fun onPause() {
        if (Updater.foreground === this) Updater.foreground = null
        super.onPause()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleReturn(intent)
    }

    override fun onDestroy() {
        readGen++
        player.stop()
        work.shutdownNow()
        super.onDestroy()
    }

    private fun client(): HttpCarrier =
        rpc ?: HttpCarrier(settings.server) { settings.token }.also { rpc = it }

    /** Runs block on a worker thread and shows a failure in status. */
    private fun background(status: TextView?, block: () -> Unit) {
        work.execute {
            try {
                block()
            } catch (e: Exception) {
                if (e is RpcException && e.code == 401) runOnUiThread { signOut("Your sign in ended. Sign in again.") }
                else runOnUiThread { status?.text = e.message ?: e.toString() }
            }
        }
    }

    private fun start() {
        val left = settings.tokenExpiresMs - System.currentTimeMillis()
        when {
            settings.token == null -> showLogin("")
            left < 2 * 3600_000 -> {
                showLogin("Refreshing the sign in...")
                background(null) {
                    try {
                        val t = client().identity.refresh(RefreshRequest())
                        settings.saveToken(t.token, t.subject, t.expiresMs.toLong())
                        runOnUiThread { showConversation() }
                    } catch (e: Exception) {
                        runOnUiThread { signOut("Sign in again.") }
                    }
                }
            }
            else -> showConversation()
        }
    }

    // --- Sign in ------------------------------------------------------------

    private fun showLogin(message: String) {
        val server = field("magi.example.com", settings.server, InputType.TYPE_TEXT_VARIATION_URI)
        val identity = field("name@example.com", settings.identity, InputType.TYPE_TEXT_VARIATION_EMAIL_ADDRESS)
        val status = TextView(this).apply { text = message }
        val go = Button(this).apply { text = "Sign in" }
        go.setOnClickListener {
            var s = server.text.toString().trim().trimEnd('/')
            if (s.isNotEmpty() && !s.contains("://")) s = "https://$s"
            val id = identity.text.toString().trim()
            val problem = when {
                s.isEmpty() -> "Enter the Magi server, for example magi.example.com."
                !s.startsWith("https://") -> "The server must use https."
                !id.contains("@") -> "Enter your identity like name@example.com."
                else -> null
            }
            if (problem != null) {
                status.text = problem
                status.setTextColor(0xFFC92A2A.toInt())
                return@setOnClickListener
            }
            status.setTextColor(identity.currentTextColor)
            server.setText(s)
            settings.server = s
            settings.identity = id
            rpc = null
            status.text = "Opening the browser..."
            background(status) {
                val b = client().identity.beginLogin(BeginLoginRequest(identity = id, client = "android", returnUrl = RETURN_URL))
                settings.pendingLogin = b.loginId to b.expiresMs.toLong()
                runOnUiThread { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(b.url))) }
            }
        }
        // The status is above the button, so the keyboard does not hide it.
        show(page(title("Magi"), label("Magi server"), server, label("Your LinkKeys identity"), identity, status, go, center = true))
    }

    /** Finishes a sign in when the browser comes back with the code. */
    private fun handleReturn(intent: Intent?): Boolean {
        val data = intent?.data ?: return false
        if (data.scheme != "community.catalyst.magi") return false
        val code = data.getQueryParameter("code")
        val pending = settings.pendingLogin
        showLogin("Finishing the sign in...")
        if (pending == null || pending.second < System.currentTimeMillis()) {
            showLogin("This sign in is not from this app or it expired. Sign in again.")
            return true
        }
        work.execute {
            try {
                val r = client().identity.pollLogin(PollLoginRequest(loginId = pending.first, waitMs = 25_000UL, code = code))
                val token = r.token
                if (r.status != "complete" || token == null) throw RpcException(0, r.message ?: "The sign in is ${r.status}.")
                settings.saveToken(token, r.subject ?: "", r.expiresMs?.toLong() ?: 0)
                runOnUiThread { showConversation() }
            } catch (e: Exception) {
                runOnUiThread { showLogin("The sign in failed: ${e.message}") }
            } finally {
                settings.pendingLogin = null
            }
        }
        return true
    }

    private fun signOut(message: String) {
        readGen++
        settings.clearToken()
        showLogin(message)
    }

    // --- Conversation -------------------------------------------------------

    private fun nav(current: String): LinearLayout {
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        for ((name, action) in listOf("Talk" to { showConversation() }, "Review" to ::showReviews, "Network" to ::showNetwork, "Tasks" to ::showTasks)) {
            row.addView(Button(this).apply {
                text = name
                isEnabled = name != current
                setOnClickListener { readGen++; action() }
            })
        }
        row.addView(Button(this).apply { text = "Sign out"; setOnClickListener { signOut("") } })
        return row
    }

    private fun personaName(id: String) = personas.firstOrNull { it.id == id }?.name ?: id.removePrefix("persona:")

    private fun day(ms: ULong) = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(ms.toLong()))

    private fun showConversation(id: String = persona) {
        readGen++
        persona = id
        lastSeq = 0UL
        shown.clear()
        messages = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(0, 8, 0, 8) }
        scroll = ScrollView(this).apply { addView(messages) }
        val input = EditText(this).apply {
            hint = "Message"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            layoutParams = LinearLayout.LayoutParams(0, WRAP_CONTENT, 1f)
        }
        val status = TextView(this)
        val send = Button(this).apply { text = "Send" }
        val mic = Button(this).apply { text = "Talk" }
        val speak = CheckBox(this).apply {
            text = "Speak replies"
            isChecked = settings.speakReplies
            setOnCheckedChangeListener { _, on -> settings.speakReplies = on }
        }
        send.setOnClickListener {
            val text = input.text.toString().trim()
            if (text.isEmpty()) return@setOnClickListener
            input.setText("")
            voiceTurn = false
            say(SayRequest(persona = persona, text = text, speakReply = speak.isChecked), status)
        }
        mic.setOnClickListener {
            if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), MIC_REQUEST)
                return@setOnClickListener
            }
            if (!recorder.recording) {
                player.stop()
                try {
                    recorder.start()
                    mic.text = "Stop"
                    status.text = "Listening..."
                } catch (e: Exception) {
                    status.text = "The microphone did not start: ${e.message}"
                }
            } else {
                mic.text = "Talk"
                val audio = recorder.stop()
                if (audio == null) {
                    status.text = "Nothing was recorded."
                    return@setOnClickListener
                }
                voiceTurn = true
                say(SayRequest(persona = persona, audio = audio, audioType = "audio/ogg", speakReply = true), status)
            }
        }
        val personaRow = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        val bar = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL; addView(input); addView(send); addView(mic) }
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(24, 24, 24, 24)
            addView(nav("Talk"))
            addView(personaRow)
            addView(scroll, LinearLayout.LayoutParams(MATCH_PARENT, 0, 1f))
            addView(status)
            addView(speak)
            addView(bar)
        }
        show(root)
        val gen = readGen
        background(status) {
            personas = runCatching { client().magi.listPersonas(ListPersonasRequest()).personas }.getOrDefault(emptyList())
            val first = client().magi.read(ReadRequest(persona = persona, beforeSeq = 1UL shl 52, limit = 50UL))
            runOnUiThread {
                if (gen != readGen) return@runOnUiThread
                if (personas.size > 1) for (p in personas) personaRow.addView(Button(this).apply {
                    text = p.name
                    isAllCaps = false
                    isEnabled = p.id != persona
                    setOnClickListener { showConversation(p.id) }
                })
                input.hint = "Message " + personaName(persona)
                first.messages.forEach { show(it, false) }
                lastSeq = first.lastSeq
                follow(gen)
            }
        }
    }

    private fun say(req: SayRequest, status: TextView) {
        status.text = "Sending..."
        background(status) {
            client().magi.say(req)
            runOnUiThread { status.text = "" }
        }
    }

    /** Long-polls the stream and shows new messages until the view changes. */
    private fun follow(gen: Int) {
        work.execute {
            while (gen == readGen) {
                try {
                    val r = client().magi.read(ReadRequest(persona = persona, afterSeq = lastSeq, limit = 100UL, waitMs = 25_000UL))
                    if (gen != readGen) return@execute
                    if (r.messages.isNotEmpty()) {
                        lastSeq = r.messages.last().seq
                        runOnUiThread { if (gen == readGen) r.messages.forEach { show(it, true) } }
                    }
                } catch (e: Exception) {
                    if (e is RpcException && e.code == 401) {
                        runOnUiThread { signOut("Your sign in ended. Sign in again.") }
                        return@execute
                    }
                    Thread.sleep(3_000)
                }
            }
        }
    }

    private fun messageView(m: Message, compact: Boolean = false): LinearLayout {
        val who = when (m.authorKind) {
            "human" -> if (m.author == settings.subject) "You" else m.author.removePrefix("user:")
            "persona" -> personaName(m.persona.ifEmpty { m.author })
            else -> m.author
        }
        val meta = mutableListOf(who, day(m.createdMs))
        if (!compact) m.generatedBy?.let { meta.add(it) }
        if (m.visibility == "shared") meta.add("shared")
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(0, 12, 0, 12) }
        box.addView(TextView(this).apply { text = meta.joinToString(" · "); setTypeface(typeface, Typeface.BOLD) })
        box.addView(TextView(this).apply { text = m.text; setTextIsSelectable(!compact) })
        for (l in m.links) {
            if (l.kind == "reply-to" && shown.containsKey(l.target.ref)) continue
            box.addView(aboutView(l))
        }
        if (!compact) box.setOnLongClickListener { audit(m); true }
        return box
    }

    private fun aboutView(l: MessageLink): TextView {
        val label = l.kind.replace('-', ' ')
        val target = when {
            l.target.kind == "utterance" -> "“${l.snippet ?: "earlier message"}”" + (l.targetMs?.let { " · " + day(it) } ?: "")
            else -> "${l.target.kind.replace('-', ' ')} ${l.target.title ?: l.target.ref}"
        }
        return TextView(this).apply {
            text = "$label: $target"
            textSize = 12f
            alpha = 0.7f
            setPadding(16, 4, 0, 0)
            if (l.target.kind == "utterance") setOnClickListener { goTo(l.target.ref) }
        }
    }

    /** Shows an earlier message: in place when it is on the page, else with its trace. */
    private fun goTo(id: String) {
        shown[id]?.let { v -> scroll.post { scroll.smoothScrollTo(0, v.top) }; return }
        background(null) {
            val t = client().magi.trace(TraceRequest(message = id, direction = "both", depth = 2UL))
            runOnUiThread {
                val col = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24, 24, 24, 24) }
                t.messages.sortedBy { it.createdMs }.forEach { col.addView(messageView(it, true)) }
                AlertDialog.Builder(this).setTitle("Context").setView(ScrollView(this).apply { addView(col) }).setPositiveButton("Close", null).show()
            }
        }
    }

    private fun show(m: Message, live: Boolean) {
        if (shown.containsKey(m.id)) return
        val box = messageView(m)
        val audio = m.audio
        if (audio != null) {
            box.addView(Button(this).apply { text = "Play"; setOnClickListener { playAudio(audio) } })
            // Speak a reply to a voice message, or when spoken replies are on.
            if (live && m.authorKind == "persona" && (voiceTurn || settings.speakReplies)) playAudio(audio)
        }
        shown[m.id] = box
        messages.addView(box)
        scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
    }

    /** Shows the links of a message and who can read it, for correction. */
    private fun audit(m: Message) {
        background(null) {
            val links = client().magi.listLinks(ListLinksRequest(message = m.id, all = true)).links
            runOnUiThread {
                val col = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24, 24, 24, 24) }
                lateinit var dialog: AlertDialog
                if (links.isEmpty()) col.addView(TextView(this).apply { text = "No links." })
                for (l in links) {
                    col.addView(TextView(this).apply {
                        text = "${l.kind} → ${l.snippet?.let { "“$it”" } ?: (l.target.title ?: l.target.ref)}\n${l.state}, by ${l.author.substringAfter(':')}, ${(l.confidence * 100).toInt()}%"
                        setPadding(0, 12, 0, 4)
                    })
                    if (l.state == "current" || l.state == "tentative") col.addView(Button(this).apply {
                        text = "Not related"
                        setOnClickListener {
                            background(null) { client().magi.unlink(UnlinkRequest(link = l.id)) }
                            dialog.dismiss()
                        }
                    })
                }
                val p = personas.firstOrNull { it.id == m.persona.ifEmpty { persona } }
                if (p != null && p.sharing != "never") {
                    val other = if (m.visibility == "shared") "private" else "shared"
                    col.addView(TextView(this).apply {
                        text = "Visibility: ${m.visibility}" + if (m.visibilityBy.isEmpty()) " (not classified yet)" else " (by ${m.visibilityBy.substringAfter(':')})"
                        setPadding(0, 24, 0, 4)
                    })
                    col.addView(Button(this).apply {
                        text = if (other == "shared") "Share with the household" else "Make private"
                        setOnClickListener {
                            background(null) { client().magi.setVisibility(SetVisibilityRequest(message = m.id, visibility = other)) }
                            dialog.dismiss()
                        }
                    })
                }
                dialog = AlertDialog.Builder(this).setTitle("Links").setView(ScrollView(this).apply { addView(col) }).setPositiveButton("Close", null).show()
            }
        }
    }

    // --- Review -------------------------------------------------------------

    /**
     * The questions of the librarian. It asks only when it is not sure; each
     * answer becomes an example for it and moves the level at which it asks.
     */
    private fun showReviews() {
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        val status = TextView(this).apply { text = "Loading..." }
        show(page(nav("Review"), status, list))
        background(status) {
            val reviews = client().magi.listReviews(ListReviewsRequest(limit = 50UL)).reviews
            runOnUiThread {
                status.text = if (reviews.isEmpty()) "Nothing to review. The librarian asks here when it is not sure how a message relates to an earlier one, or who can read it." else ""
                for (r in reviews) list.addView(reviewView(r, list, status))
            }
        }
    }

    private fun reviewView(r: ReviewItem, list: LinearLayout, status: TextView): View {
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(0, 24, 0, 24) }
        box.addView(TextView(this).apply { text = r.question + " (${(r.confidence * 100).toInt()}% sure)"; setTypeface(typeface, Typeface.BOLD) })
        box.addView(messageView(r.message, true))
        r.target?.let { box.addView(TextView(this).apply { text = "Earlier:"; alpha = 0.7f }); box.addView(messageView(it, true)) }
        val note = EditText(this).apply { hint = "Why (optional)"; isSingleLine = true }
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        fun answer(a: String) = background(status) {
            client().magi.answerReview(AnswerReviewRequest(id = r.id, answer = a, note = note.text.toString().ifBlank { null }))
            runOnUiThread { list.removeView(box) }
        }
        for (c in r.choices) row.addView(Button(this).apply {
            isAllCaps = false
            text = when {
                r.task == "visibility" -> if (c == "shared") "Household" else "Only me"
                c == "unrelated" -> "Not related"
                else -> "Yes"
            }
            setOnClickListener { answer(c) }
        })
        row.addView(Button(this).apply { text = "Skip"; isAllCaps = false; setOnClickListener { answer("dismiss") } })
        box.addView(row)
        box.addView(note)
        return box
    }

    /**
     * Requests that the egress proxy blocked for review. Only admins see
     * them. A decision is a rule for the next such requests. Placeholders
     * stand in for secrets, so a review never shows a secret.
     */
    private fun showNetwork() {
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        val status = TextView(this).apply { text = "Loading..." }
        show(page(nav("Network"), title("Network reviews"), status, list))
        background(status) {
            val reviews = client().magi.listEgressReviews(ListEgressReviewsRequest(limit = 100UL)).reviews
            runOnUiThread {
                status.text = if (reviews.isEmpty()) "No open reviews." else "The egress proxy blocked these requests of tasks. Your decision is a rule for the next such requests."
                for (r in reviews) list.addView(netReviewView(r, list, status))
            }
        }
    }

    private fun netReviewView(r: EgressReview, list: LinearLayout, status: TextView): View {
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(0, 24, 0, 24) }
        val host = r.destination.removeSuffix(":443")
        box.addView(TextView(this).apply {
            text = if (r.kind == "destination") "Destination ${r.destination}" else "${r.method} ${r.url}"
            setTypeface(typeface, Typeface.BOLD); setTextIsSelectable(true)
        })
        box.addView(TextView(this).apply { text = "${r.reason} (${r.by}; ${r.count} times, last ${day(r.lastMs)}; ${r.owner})" })
        if (r.headers.isNotEmpty() || r.body.isNotEmpty()) {
            val body = try {
                Charsets.UTF_8.newDecoder().decode(java.nio.ByteBuffer.wrap(r.body)).toString()
            } catch (e: java.nio.charset.CharacterCodingException) { "(binary body, ${r.body.size} bytes)" }
            box.addView(TextView(this).apply {
                text = (r.headers.map { "${it.name}: ${it.value}" } + listOf("", body)).joinToString("\n").trim()
                typeface = Typeface.MONOSPACE; textSize = 12f; alpha = 0.8f; setTextIsSelectable(true)
            })
        }
        val note = EditText(this).apply { hint = "Note (optional)"; isSingleLine = true }
        fun decide(d: String) = background(status) {
            val res = client().magi.decideEgressReview(DecideEgressReviewRequest(id = r.id, decision = d, note = note.text.toString().ifBlank { null }))
            runOnUiThread {
                list.removeView(box)
                status.text = res.rule?.let { "Rule: $it." } ?: "Dismissed. The next blocked request opens a review again."
            }
        }
        val choices = if (r.kind == "destination") listOf("Permit" to "allow-host", "Permit at layer 4" to "allow-layer4", "Block" to "block", "Dismiss" to "dismiss")
            else listOf("Allow ${r.method} to $host" to "allow-host", "Allow this path" to "allow-path", "Block" to "block", "Dismiss" to "dismiss")
        val row = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        for ((label, d) in choices) row.addView(Button(this).apply { text = label; isAllCaps = false; setOnClickListener { decide(d) } })
        box.addView(row)
        box.addView(note)
        return box
    }

    private fun playAudio(digest: String) {
        background(null) {
            val a = client().magi.getAudio(GetAudioRequest(digest = digest))
            runOnUiThread { player.play(a.body, a.mediaType) }
        }
    }

    // --- Tasks --------------------------------------------------------------

    private fun showTasks() {
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        val status = TextView(this).apply { text = "Loading..." }
        show(page(nav("Tasks"), status, list))
        background(status) {
            val tasks = client().magi.listTasks(ListTasksRequest(limit = 30UL)).tasks
            runOnUiThread {
                status.text = if (tasks.isEmpty()) "No tasks yet. Ask Magi to change something." else ""
                for (t in tasks) {
                    val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(0, 16, 0, 16) }
                    box.addView(TextView(this).apply { text = t.title; setTypeface(typeface, Typeface.BOLD) })
                    box.addView(TextView(this).apply { text = "${t.status} · ${t.currentStep}" + (t.error?.let { "\n$it" } ?: "") })
                    val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
                    row.addView(Button(this).apply { text = "Diff"; setOnClickListener { showDiff(t.id, status) } })
                    t.waitingFor?.let { prompt ->
                        box.addView(TextView(this).apply { text = prompt })
                        for (outcome in t.decisions ?: listOf("approve", "reject")) {
                            row.addView(Button(this).apply {
                                text = outcome
                                setOnClickListener { decide(t.id, outcome, status) }
                            })
                        }
                    }
                    if (t.status == "running" || t.status == "waiting") {
                        row.addView(Button(this).apply { text = "Stop"; setOnClickListener { cancel(t.id, status) } })
                    }
                    box.addView(row)
                    list.addView(box)
                }
            }
        }
    }

    private fun showDiff(id: String, status: TextView) {
        background(status) {
            val d = client().magi.getTaskDiff(GetTaskDiffRequest(id = id))
            runOnUiThread {
                val text = TextView(this).apply {
                    text = d.patch.ifEmpty { "No change." } + if (d.truncated) "\n[truncated]" else ""
                    typeface = Typeface.MONOSPACE
                    textSize = 11f
                    setTextIsSelectable(true)
                    setPadding(24, 24, 24, 24)
                }
                AlertDialog.Builder(this).setView(ScrollView(this).apply { addView(text) }).setPositiveButton("Close", null).show()
            }
        }
    }

    private fun decide(id: String, outcome: String, status: TextView) {
        AlertDialog.Builder(this)
            .setMessage("Record \"$outcome\" for this task?")
            .setPositiveButton(outcome) { _, _ ->
                background(status) {
                    client().magi.decide(DecideRequest(task = id, outcome = outcome))
                    runOnUiThread { showTasks() }
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    /** Stops a task. For a risky step the server tells the risk, and the person must agree. */
    private fun cancel(id: String, status: TextView) {
        fun send(confirm: Boolean): Unit = background(status) {
            try {
                client().magi.cancelTask(CancelTaskRequest(task = id, confirm = confirm))
                runOnUiThread { showTasks() }
            } catch (e: RpcException) {
                val risk = e.message?.substringAfter("confirm: ", "")
                if (e.code != 409 || risk.isNullOrEmpty() || confirm) throw e
                runOnUiThread {
                    AlertDialog.Builder(this)
                        .setMessage("Stopping this task is risky: $risk\n\nStop it anyway?")
                        .setPositiveButton("Stop") { _, _ -> send(true) }
                        .setNegativeButton("Keep running", null)
                        .show()
                }
            }
        }
        AlertDialog.Builder(this)
            .setMessage("Stop this task?")
            .setPositiveButton("Stop") { _, _ -> send(false) }
            .setNegativeButton("Keep running", null)
            .show()
    }

    // --- Views --------------------------------------------------------------

    private fun label(text: String) = TextView(this).apply { this.text = text; setPadding(0, 24, 0, 4); setTypeface(typeface, Typeface.BOLD) }

    private fun title(text: String) = TextView(this).apply { this.text = text; textSize = 24f; setPadding(0, 0, 0, 24) }

    private fun field(hint: String, value: String, type: Int) = EditText(this).apply {
        this.hint = hint
        setText(value)
        inputType = InputType.TYPE_CLASS_TEXT or type
        isSingleLine = true
    }

    private fun page(vararg views: View, center: Boolean = false): ScrollView {
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(24, 24, 24, 24)
            if (center) gravity = Gravity.CENTER_VERTICAL
        }
        views.forEach { col.addView(it) }
        return ScrollView(this).apply { isFillViewport = center; addView(col, LayoutParams(MATCH_PARENT, MATCH_PARENT)) }
    }

    /**
     * Shows a page inside the system bars and above the keyboard. Since
     * Android 15, apps draw behind the status bar unless they add this space.
     */
    private fun show(v: View) {
        val frame = FrameLayout(this)
        frame.addView(v, FrameLayout.LayoutParams(MATCH_PARENT, MATCH_PARENT))
        frame.setOnApplyWindowInsetsListener { view, insets ->
            if (Build.VERSION.SDK_INT >= 30) {
                val b = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime() or WindowInsets.Type.displayCutout())
                view.setPadding(b.left, b.top, b.right, b.bottom)
            } else {
                @Suppress("DEPRECATION")
                view.setPadding(insets.systemWindowInsetLeft, insets.systemWindowInsetTop, insets.systemWindowInsetRight, insets.systemWindowInsetBottom)
            }
            insets
        }
        setContentView(frame)
        frame.requestApplyInsets()
    }
}
