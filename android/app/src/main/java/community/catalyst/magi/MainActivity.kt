package community.catalyst.magi

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
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
import community.catalyst.magi.client.magi.DecideRequest
import community.catalyst.magi.client.magi.GetAudioRequest
import community.catalyst.magi.client.magi.GetTaskDiffRequest
import community.catalyst.magi.client.magi.ListChatsRequest
import community.catalyst.magi.client.magi.ListTasksRequest
import community.catalyst.magi.client.magi.Message
import community.catalyst.magi.client.magi.ReadRequest
import community.catalyst.magi.client.magi.SayRequest
import java.util.concurrent.Executors

/**
 * The Magi Android client. It uses only the public Magi API: IdentityService
 * to sign in, MagiService for chats, voice, and tasks.
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

    private var chat: String? = null
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
        if (!handleReturn(intent)) start()
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
                        runOnUiThread { showChats() }
                    } catch (e: Exception) {
                        runOnUiThread { signOut("Sign in again.") }
                    }
                }
            }
            else -> showChats()
        }
    }

    // --- Sign in ------------------------------------------------------------

    private fun showLogin(message: String) {
        val server = field("Magi server, for example https://magi.example.com", settings.server, InputType.TYPE_TEXT_VARIATION_URI)
        val identity = field("Your LinkKeys identity, name@example.com", settings.identity, InputType.TYPE_TEXT_VARIATION_EMAIL_ADDRESS)
        val status = TextView(this).apply { text = message }
        val go = Button(this).apply { text = "Sign in" }
        go.setOnClickListener {
            val s = server.text.toString().trim().trimEnd('/')
            val id = identity.text.toString().trim()
            if (!s.startsWith("https://") || !id.contains("@")) {
                status.text = "Enter an https server and an identity like name@example.com."
                return@setOnClickListener
            }
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
        setContentView(page(title("Magi"), server, identity, go, status))
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
                runOnUiThread { showChats() }
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

    // --- Chats --------------------------------------------------------------

    private fun nav(current: String): LinearLayout {
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        for ((name, action) in listOf("Chats" to ::showChats, "Tasks" to ::showTasks)) {
            row.addView(Button(this).apply {
                text = name
                isEnabled = name != current
                setOnClickListener { readGen++; action() }
            })
        }
        row.addView(Button(this).apply { text = "Sign out"; setOnClickListener { signOut("") } })
        return row
    }

    private fun showChats() {
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        val status = TextView(this).apply { text = "Loading..." }
        val fresh = Button(this).apply { text = "New chat"; setOnClickListener { openChat(null) } }
        setContentView(page(nav("Chats"), fresh, status, list))
        background(status) {
            val chats = client().magi.listChats(ListChatsRequest(limit = 50UL)).chats
            runOnUiThread {
                status.text = if (chats.isEmpty()) "No chats yet." else ""
                for (c in chats) {
                    list.addView(Button(this).apply {
                        text = c.title.ifEmpty { "Chat" }
                        isAllCaps = false
                        gravity = Gravity.START or Gravity.CENTER_VERTICAL
                        setOnClickListener { openChat(c.id) }
                    })
                }
            }
        }
    }

    private fun openChat(id: String?) {
        readGen++
        chat = id
        lastSeq = 0UL
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
            say(SayRequest(chat = chat, text = text, speakReply = speak.isChecked), status)
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
                say(SayRequest(chat = chat, audio = audio, audioType = "audio/ogg", speakReply = true), status)
            }
        }
        val bar = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL; addView(input); addView(send); addView(mic) }
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(24, 24, 24, 24)
            addView(nav(""))
            addView(scroll, LinearLayout.LayoutParams(MATCH_PARENT, 0, 1f))
            addView(status)
            addView(speak)
            addView(bar)
        }
        setContentView(root)
        if (id != null) follow(id, readGen)
    }

    private fun say(req: SayRequest, status: TextView) {
        status.text = "Sending..."
        background(status) {
            val r = client().magi.say(req)
            runOnUiThread {
                status.text = ""
                if (chat == null) {
                    chat = r.chat
                    follow(r.chat, readGen)
                }
            }
        }
    }

    /** Long-polls the chat and shows new messages until the chat closes. */
    private fun follow(id: String, gen: Int) {
        work.execute {
            var history = true // the first read returns old messages; do not play them
            while (gen == readGen) {
                try {
                    val r = client().magi.read(ReadRequest(chat = id, afterSeq = lastSeq, limit = 100UL, waitMs = 25_000UL))
                    if (gen != readGen) return@execute
                    if (r.messages.isNotEmpty()) {
                        lastSeq = r.messages.last().seq
                        val live = !history
                        runOnUiThread { if (gen == readGen) r.messages.forEach { show(it, live) } }
                    }
                    history = false
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

    private fun show(m: Message, live: Boolean) {
        val who = when (m.authorKind) {
            "human" -> "You"
            "persona" -> "Magi"
            else -> m.author
        }
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(0, 12, 0, 12) }
        box.addView(TextView(this).apply {
            text = who + (m.generatedBy?.let { " · $it" } ?: "")
            setTypeface(typeface, Typeface.BOLD)
        })
        box.addView(TextView(this).apply { text = m.text; setTextIsSelectable(true) })
        val audio = m.audio
        if (audio != null) {
            box.addView(Button(this).apply { text = "Play"; setOnClickListener { playAudio(audio) } })
            // Speak a reply to a voice message, or when spoken replies are on.
            if (live && m.authorKind == "persona" && (voiceTurn || settings.speakReplies)) playAudio(audio)
        }
        messages.addView(box)
        scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
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
        setContentView(page(nav("Tasks"), status, list))
        background(status) {
            val tasks = client().magi.listTasks(ListTasksRequest(limit = 30UL)).tasks
            runOnUiThread {
                status.text = if (tasks.isEmpty()) "No tasks yet. Ask Magi in a chat to change something." else ""
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

    // --- Views --------------------------------------------------------------

    private fun title(text: String) = TextView(this).apply { this.text = text; textSize = 24f; setPadding(0, 0, 0, 24) }

    private fun field(hint: String, value: String, type: Int) = EditText(this).apply {
        this.hint = hint
        setText(value)
        inputType = InputType.TYPE_CLASS_TEXT or type
        isSingleLine = true
    }

    private fun page(vararg views: View): ScrollView {
        val col = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(24, 24, 24, 24) }
        views.forEach { col.addView(it) }
        return ScrollView(this).apply { addView(col) }
    }
}
