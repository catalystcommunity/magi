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
import android.widget.Toast
import android.content.ClipData
import android.content.ClipboardManager
import android.content.res.ColorStateList
import android.text.Editable
import android.text.TextWatcher
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.PopupMenu
import android.widget.ProgressBar
import community.catalyst.magi.Ui.Kind
import community.catalyst.magi.client.identity.BeginLoginRequest
import community.catalyst.magi.client.identity.PollLoginRequest
import community.catalyst.magi.client.identity.RefreshRequest
import community.catalyst.magi.client.magi.ActivityRequest
import community.catalyst.magi.client.magi.CancelTaskRequest
import community.catalyst.magi.client.magi.DecideEgressReviewRequest
import community.catalyst.magi.client.magi.DecideRequest
import community.catalyst.magi.client.magi.EgressReview
import community.catalyst.magi.client.magi.GetAccessRequest
import community.catalyst.magi.client.magi.GetAudioRequest
import community.catalyst.magi.client.magi.GetTaskDiffRequest
import community.catalyst.magi.client.magi.AnswerReviewRequest
import community.catalyst.magi.client.magi.ListEgressReviewsRequest
import community.catalyst.magi.client.magi.ListLinksRequest
import community.catalyst.magi.client.magi.ListPersonasRequest
import community.catalyst.magi.client.magi.ListReviewsRequest
import community.catalyst.magi.client.magi.ListTasksRequest
import community.catalyst.magi.client.magi.ListenRequest
import community.catalyst.magi.client.magi.ReportDeliveryRequest
import community.catalyst.magi.client.magi.StopSpeechRequest
import community.catalyst.magi.client.magi.Message
import community.catalyst.magi.client.magi.MessageLink
import community.catalyst.magi.client.magi.PersonaInfo
import community.catalyst.magi.client.magi.ReadRequest
import community.catalyst.magi.client.magi.ReviewItem
import community.catalyst.magi.client.magi.SayRequest
import community.catalyst.magi.client.magi.SetVisibilityRequest
import community.catalyst.magi.client.magi.TraceRequest
import community.catalyst.magi.client.magi.UnlinkRequest
import community.catalyst.magi.client.magi.CheckOverlapRequest
import community.catalyst.magi.client.magi.Notice
import community.catalyst.magi.client.magi.SpeechPart
import android.media.AudioManager
import android.media.ToneGenerator
import android.os.Handler
import android.os.Looper
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
        const val NOTIFY_REQUEST = 2
        const val WORKING_MS = 3 * 60 * 1000L
    }

    internal lateinit var settings: Settings
    private lateinit var player: Player
    private lateinit var handsFree: HandsFree
    private val main = Handler(Looper.getMainLooper())
    @Volatile private var hearing = false
    private var micButton: ImageView? = null
    internal lateinit var ui: Ui
    /** The tabs at the bottom. The keyboard hides them. */
    private var navBar: View? = null
    private var barTitle: TextView? = null
    private var barAvatar: TextView? = null
    /** The author and the time of the newest bubble, for the groups and the day lines. */
    private var lastAuthor: String? = null
    private var lastShownMs = 0L
    private var lastId: String? = null
    /** Where the back button goes. Without it, the back button leaves the app. */
    internal var back: (() -> Unit)? = null
        set(v) {
            field = v
            // Since Android 13 the platform asks a callback, and an app for
            // Android 16 does not get onBackPressed.
            if (Build.VERSION.SDK_INT >= 33) {
                val callback = backCallback as android.window.OnBackInvokedCallback
                onBackInvokedDispatcher.unregisterOnBackInvokedCallback(callback)
                if (v != null) onBackInvokedDispatcher.registerOnBackInvokedCallback(android.window.OnBackInvokedDispatcher.PRIORITY_DEFAULT, callback)
            }
        }
    private val backCallback: Any? =
        if (Build.VERSION.SDK_INT >= 33) android.window.OnBackInvokedCallback { back?.let { readGen++; it() } } else null
    private var noticeBar: LinearLayout? = null
    private val work = Executors.newCachedThreadPool()
    private var rpc: HttpCarrier? = null

    private var persona = "persona:magi"
    private var personas: List<PersonaInfo> = emptyList()
    private val shown = HashMap<String, View>()
    @Volatile private var lastSeq = 0UL
    @Volatile private var readGen = 0
    @Volatile private var voiceTurn = false
    private lateinit var messages: LinearLayout
    /** Shows that Magi builds a reply to the newest message of a person. */
    private var workingRow: View? = null
    private var workingText: TextView? = null
    /** When the activity of Magi last said that it works on a reply. */
    private var activitySeen = 0L
    private val pollActivity = Runnable { readActivity() }
    private var waitingSince = 0L
    private val hideWorking = Runnable { updateWorking() }
    private lateinit var scroll: ScrollView

    override fun onCreate(state: Bundle?) {
        settings = Settings(this)
        // The theme comes before the first view. Dark is the default.
        val dark = Palette.dark(settings.theme, this)
        setTheme(if (dark) R.style.Magi else R.style.Magi_Light)
        super.onCreate(state)
        ui = Ui(this, Palette(dark))
        player = Player(this)
        player.onIdle = { speechEnded("done") }
        handsFree = HandsFree(this, voice)
        runCatching { Updater.schedule(this) }.onFailure { android.util.Log.w("MagiUpdater", "schedule: $it") }
        if (!handleReturn(intent)) start()
        openNotice(intent)
    }

    override fun onResume() {
        super.onResume()
        Updater.foreground = this
        Attention.foreground = ::onNotice
        Attention.closed = { ids -> runOnUiThread { ids.forEach { removeNotice(it) } } }
        work.execute { runCatching { Updater.check(applicationContext) } }
        if (settings.token != null) background(null) { Attention.hello(this, client()) }
    }

    override fun onPause() {
        if (Updater.foreground === this) Updater.foreground = null
        // Hands-free talk keeps the notices here: the person still talks with the app.
        if (!handsFree.on) {
            Attention.foreground = null
            Attention.closed = null
        }
        if (settings.token != null) background(null) { Attention.hello(this, client()) }
        super.onPause()
    }

    @Deprecated("The platform has a newer callback; this one works on each version that the app supports.")
    override fun onBackPressed() {
        val b = back
        if (b == null) {
            @Suppress("DEPRECATION")
            super.onBackPressed()
        } else {
            readGen++
            b()
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        if (!handleReturn(intent)) openNotice(intent)
    }

    override fun onDestroy() {
        readGen++
        handsFree.stop()
        Attention.handsFree = false
        if (Attention.foreground == ::onNotice) Attention.foreground = null
        player.stop()
        work.shutdownNow()
        super.onDestroy()
    }

    internal fun client(): HttpCarrier =
        rpc ?: HttpCarrier(settings.server) { settings.token }.also { rpc = it }

    /** Runs block on a worker thread and shows a failure in status. */
    internal fun background(status: TextView?, block: () -> Unit) {
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
        navBar = null
        back = null
        val server = ui.field("magi.example.com", settings.server, InputType.TYPE_TEXT_VARIATION_URI)
        val identity = ui.field("name@example.com", settings.identity, InputType.TYPE_TEXT_VARIATION_EMAIL_ADDRESS)
        val status = ui.text(message, 14f, ui.p.muted).apply { setPadding(0, ui.dp(14), 0, ui.dp(14)) }
        lateinit var go: TextView
        go = ui.button("Sign in", Kind.PRIMARY) {
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
                status.setTextColor(ui.p.danger)
                return@button
            }
            status.setTextColor(ui.p.muted)
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
        val logo = ImageView(this).apply {
            setImageResource(R.mipmap.ic_launcher_monochrome)
            imageTintList = ColorStateList.valueOf(ui.p.text)
        }
        // The status is above the button, so the keyboard does not hide it.
        val col = ui.column(24).apply {
            gravity = Gravity.CENTER_VERTICAL
            addView(logo, LinearLayout.LayoutParams(ui.dp(150), ui.dp(150)).apply { gravity = Gravity.CENTER_HORIZONTAL })
            addView(ui.text("Magi", 28f, bold = true).apply { gravity = Gravity.CENTER; setPadding(0, 0, 0, ui.dp(20)) })
            addView(label("Magi server"))
            addView(server)
            addView(label("Your LinkKeys identity"))
            addView(identity)
            addView(status)
            addView(go)
        }
        show(ScrollView(this).apply { isFillViewport = true; addView(col, LayoutParams(MATCH_PARENT, MATCH_PARENT)) })
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

    /** The tabs at the bottom of each page. */
    private fun nav(current: String): LinearLayout {
        val bar = ui.row().apply { setBackgroundColor(ui.p.surface); setPadding(0, ui.dp(6), 0, ui.dp(6)) }
        val tabs = listOf<Triple<String, Int, () -> Unit>>(
            Triple("Talk", R.drawable.ic_chat, { showConversation() }),
            Triple("Review", R.drawable.ic_review, { showReviews() }),
            Triple("Network", R.drawable.ic_network, { showNetwork() }),
            Triple("Tasks", R.drawable.ic_tasks, { showTasks() }),
            Triple("Admin", R.drawable.ic_admin, { showAdmin() }),
        // Only an operator sees the network reviews and the admin views.
        ).filter { settings.operator || (it.first != "Network" && it.first != "Admin") }
        for ((name, icon, action) in tabs) {
            val color = if (name == current) ui.p.accent else ui.p.muted
            val item = ui.column().apply {
                gravity = Gravity.CENTER
                addView(ImageView(this@MainActivity).apply { setImageResource(icon); imageTintList = ColorStateList.valueOf(color) },
                    LinearLayout.LayoutParams(ui.dp(24), ui.dp(24)))
                addView(ui.text(name, 11f, color, bold = name == current).apply { gravity = Gravity.CENTER })
                isClickable = true
                setOnClickListener { if (name != current) { readGen++; action() } }
            }
            bar.addView(item, LinearLayout.LayoutParams(0, WRAP_CONTENT, 1f))
        }
        navBar = bar
        return bar
    }

    /** The bar at the top of a page: the title and the menu. */
    private fun topBar(title: String, avatar: Boolean = false): LinearLayout {
        val bar = ui.row().apply { setPadding(ui.dp(16), ui.dp(8), ui.dp(4), ui.dp(8)) }
        barAvatar = null
        if (avatar) {
            bar.addView(ui.avatar(title).also { barAvatar = it })
            bar.addView(ui.gap(10))
        }
        bar.addView(ui.text(title, 20f, bold = true).also { barTitle = it }, LinearLayout.LayoutParams(0, WRAP_CONTENT, 1f))
        lateinit var more: ImageView
        more = ui.icon(R.drawable.ic_more, "Menu") { menu(more) }
        bar.addView(more)
        return bar
    }

    /** The settings of the app and the sign out. */
    private fun menu(anchor: View) {
        val m = PopupMenu(this, anchor)
        m.menu.add("Speak replies").apply { isCheckable = true; isChecked = settings.speakReplies }
            .setOnMenuItemClickListener { settings.speakReplies = !settings.speakReplies; true }
        m.menu.add("Wake word (“Computer, …”)").apply { isCheckable = true; isChecked = settings.wakeWord }
            .setOnMenuItemClickListener { settings.wakeWord = !settings.wakeWord; true }
        val theme = m.menu.addSubMenu("Theme")
        for ((label, value) in listOf("Dark" to "dark", "Light" to "light", "As the phone" to "system")) {
            theme.add(label).apply { isCheckable = true; isChecked = settings.theme == value }
                .setOnMenuItemClickListener { settings.theme = value; recreate(); true }
        }
        m.menu.add("Check for updates").setOnMenuItemClickListener { checkForUpdates(); true }
        m.menu.add("About").setOnMenuItemClickListener { about(); true }
        m.menu.add("Sign out").setOnMenuItemClickListener { signOut(""); true }
        m.show()
    }

    /** Asks Magi for a newer build now and tells the result. */
    private fun checkForUpdates() {
        Toast.makeText(this, "Checking for updates...", Toast.LENGTH_SHORT).show()
        work.execute {
            val result = runCatching { Updater.check(applicationContext) }
            val text = result.fold({ r ->
                when {
                    r.startsWith("up to date") -> "This is the newest build."
                    r.startsWith("installing") -> "A newer build installs now. The app starts again."
                    r == "no release" -> "The server has no app build."
                    r == "not signed in" -> "Sign in to check for updates."
                    else -> r
                }
            }, { e -> "The check failed: ${e.message ?: e}" })
            runOnUiThread { Toast.makeText(this, text, Toast.LENGTH_LONG).show() }
        }
    }

    /** The version of the app, the time of its build, and the server. */
    private fun about() {
        val info = packageManager.getPackageInfo(packageName, 0)
        val built = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(ChatTime.buildMs(info.longVersionCode)))
        val col = ui.column(20)
        for ((name, value) in listOf(
            "Version" to "${info.versionName} (${info.longVersionCode})",
            "Built" to built,
            "Server" to settings.server.ifEmpty { "not set" },
            "Signed in as" to settings.identity.ifEmpty { "not signed in" },
        )) {
            col.addView(ui.text(name, 12f, ui.p.muted).apply { setPadding(0, ui.dp(8), 0, 0) })
            col.addView(ui.text(value, 16f).apply { setTextIsSelectable(true) })
        }
        AlertDialog.Builder(this).setTitle("Magi").setView(col).setPositiveButton("Close", null).show()
    }

    internal fun personaName(id: String) = personas.firstOrNull { it.id == id }?.name ?: id.removePrefix("persona:").replaceFirstChar { it.uppercase() }

    internal fun day(ms: ULong) = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(ms.toLong()))

    private fun showConversation(id: String = persona) {
        readGen++
        back = null
        persona = id
        lastSeq = 0UL
        shown.clear()
        lastAuthor = null
        lastShownMs = 0L
        lastId = null
        // A short conversation stays at the bottom, next to the field.
        messages = ui.column().apply { setPadding(ui.dp(10), ui.dp(8), ui.dp(10), ui.dp(8)); gravity = Gravity.BOTTOM }
        scroll = ScrollView(this).apply { isFillViewport = true; addView(messages, LayoutParams(MATCH_PARENT, WRAP_CONTENT)) }
        // A new bubble keeps the newest text in view when the person reads
        // the end of the conversation, and not when the person reads above.
        // The keyboard makes the list shorter: the end stays in view.
        scroll.addOnLayoutChangeListener { _, _, top, _, bottom, _, oldTop, _, oldBottom ->
            if (bottom - top < oldBottom - oldTop) scroll.post { scroll.scrollTo(0, messages.height) }
        }
        messages.addOnLayoutChangeListener { _, _, _, _, bottom, _, _, _, oldBottom ->
            if (bottom != oldBottom && scroll.scrollY + scroll.height >= oldBottom - ui.dp(120)) scroll.post { scroll.scrollTo(0, messages.height) }
        }
        val input = ui.field("Message", lines = true).apply { layoutParams = LinearLayout.LayoutParams(0, WRAP_CONTENT, 1f) }
        val status = ui.text("", 13f, ui.p.muted).apply { setPadding(ui.dp(16), 0, ui.dp(16), ui.dp(4)); visibility = View.GONE }
        // The line is there only when it has a text.
        status.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
            override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
            override fun afterTextChanged(s: Editable?) { status.visibility = if (s.isNullOrEmpty()) View.GONE else View.VISIBLE }
        })
        val working = ui.row().apply {
            setPadding(ui.dp(12), ui.dp(8), ui.dp(14), ui.dp(8))
            background = ui.bubble(ui.p.raised, false)
            addView(ProgressBar(this@MainActivity, null, android.R.attr.progressBarStyleSmall))
            addView(ui.text("${personaName(persona)} is working on it", 14f, ui.p.muted).apply { setPadding(ui.dp(10), 0, 0, 0); workingText = this })
        }
        val workingWrap = FrameLayout(this).apply {
            visibility = View.GONE
            setPadding(ui.dp(10), 0, ui.dp(10), ui.dp(6))
            addView(working, FrameLayout.LayoutParams(WRAP_CONTENT, WRAP_CONTENT))
        }
        workingRow = workingWrap
        waitingSince = 0L
        val mic = ui.icon(R.drawable.ic_mic, "Hands-free talk", bg = ui.p.raised) { toggleHandsFree(status) }
        val send = ui.icon(R.drawable.ic_send, "Send", tint = ui.p.onAccent, bg = ui.p.accent) {
            val text = input.text.toString().trim()
            if (text.isNotEmpty()) {
                input.setText("")
                voiceTurn = false
                say(SayRequest(persona = persona, text = text, speakReply = settings.speakReplies), status)
            }
        }
        for (b in listOf(mic, send)) (b.layoutParams as LinearLayout.LayoutParams).leftMargin = ui.dp(6)
        micButton = mic
        updateMic()
        val personaRow = ui.row().apply { setPadding(ui.dp(12), 0, ui.dp(12), ui.dp(6)) }
        val composer = ui.row().apply {
            gravity = Gravity.BOTTOM
            setPadding(ui.dp(10), ui.dp(4), ui.dp(10), ui.dp(8))
            addView(input); addView(mic); addView(send)
        }
        val bar0 = ui.column().apply { setPadding(ui.dp(10), 0, ui.dp(10), 0) }
        noticeBar = bar0
        val root = ui.column().apply {
            addView(topBar(personaName(persona), avatar = true))
            addView(HorizontalScrollView(this@MainActivity).apply { isHorizontalScrollBarEnabled = false; addView(personaRow) })
            addView(bar0)
            addView(scroll, LinearLayout.LayoutParams(MATCH_PARENT, 0, 1f))
            addView(workingWrap)
            addView(status)
            addView(composer)
            addView(nav("Talk"))
        }
        show(root)
        Attention.start(this)
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED && !settings.askedNotify) {
            settings.askedNotify = true
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), NOTIFY_REQUEST)
        }
        val gen = readGen
        background(status) {
            personas = runCatching { client().magi.listPersonas(ListPersonasRequest()).personas }.getOrDefault(emptyList())
            // The tabs come from the role of the last visit. A changed role builds the page again.
            val operator = runCatching { client().magi.getAccess(GetAccessRequest()).operator }.getOrDefault(settings.operator)
            if (operator != settings.operator) {
                settings.operator = operator
                runOnUiThread { if (gen == readGen) showConversation() }
                return@background
            }
            val first = client().magi.read(ReadRequest(persona = persona, beforeSeq = 1UL shl 52, limit = 50UL))
            runOnUiThread {
                if (gen != readGen) return@runOnUiThread
                if (personas.size > 1) for (p in personas) {
                    if (personaRow.childCount > 0) personaRow.addView(ui.gap(8))
                    personaRow.addView(ui.chip(p.name, p.id == persona) { if (p.id != persona) showConversation(p.id) })
                }
                val name = personaName(persona)
                barTitle?.text = name
                barAvatar?.text = name.take(1).uppercase()
                workingText?.text = "$name is working on it"
                input.hint = "Message $name"
                first.messages.forEach { show(it, false) }
                lastSeq = first.lastSeq
                follow(gen)
            }
        }
    }

    /** Starts or stops hands-free talk. */
    private fun toggleHandsFree(status: TextView) {
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), MIC_REQUEST)
            return
        }
        if (handsFree.on) {
            handsFree.stop()
            player.communication = false
            Attention.handsFree = false
            Attention.start(this, mic = false)
            status.text = ""
        } else {
            try {
                player.communication = true
                handsFree.start()
                Attention.handsFree = true
                Attention.start(this, mic = true)
                status.text = "Hands-free: talk at any time. Talk over Magi to stop it."
            } catch (e: Exception) {
                player.communication = false
                status.text = "The microphone did not start: ${e.message}"
            }
        }
        updateMic()
        background(null) { Attention.hello(this, client()) }
    }

    /** The microphone button shows the state: off, listening, or hearing the person. */
    private fun updateMic() {
        val mic = micButton ?: return
        val on = handsFree.on
        mic.background = ui.shape(if (!on) ui.p.raised else if (hearing) ui.p.danger else ui.p.accent, 22)
        mic.imageTintList = ColorStateList.valueOf(if (on) ui.p.onAccent else ui.p.text)
        mic.contentDescription = if (!on) "Hands-free talk" else if (hearing) "Hearing you" else "Listening"
    }

    private fun say(req: SayRequest, status: TextView) {
        status.text = "Sending..."
        background(status) {
            val r = client().magi.say(req)
            if (r.ignored == true) { runOnUiThread { status.text = "" }; return@background }
            // The tone says that Magi heard the person and builds the reply:
            // for speech, and for text when replies are spoken.
            if (req.audio != null || req.speakReply == true) runOnUiThread { Ack.play(player.communication) }
            // A message that names another persona ("Engineering, ...") went to that persona.
            val p = r.message.persona
            runOnUiThread { status.text = ""; if (p.isNotEmpty() && p != persona) showConversation(p) }
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

    /**
     * A message as a bubble: on the right for the person who uses the app,
     * on the left for each other author. named puts the author above the
     * text. A compact bubble is for a dialog or a card: it has the author
     * and the date, and no actions.
     */
    internal fun messageView(m: Message, compact: Boolean = false, named: Boolean = true, right: Boolean? = null, onPlay: (() -> Unit)? = null): LinearLayout {
        val own = m.authorKind == "human" && m.author == settings.subject
        // right puts a bubble on a side by a rule of the caller (an admin view).
        val mine = right ?: own
        val who = when (m.authorKind) {
            "human" -> if (own) "You" else m.author.removePrefix("user:")
            "persona" -> personaName(m.persona.ifEmpty { m.author })
            else -> m.author
        }
        val fg = if (mine) ui.p.onMine else ui.p.text
        val soft = if (mine) ui.p.onMineMuted else ui.p.muted
        val width = (resources.displayMetrics.widthPixels * 0.78f).toInt()
        val box = ui.column().apply {
            background = ui.bubble(if (mine) ui.p.mine else ui.p.raised, mine)
            setPadding(ui.dp(12), ui.dp(8), ui.dp(12), ui.dp(7))
        }
        if (compact || (named && !mine)) box.addView(ui.text(who, 13f, if (mine) soft else ui.p.accent, bold = true))
        for (l in m.links) {
            if (l.kind == "reply-to" && shown.containsKey(l.target.ref)) continue
            // The bubble above is the message that this one answers: no quote.
            if (!compact && l.target.kind == "utterance" && l.target.ref == lastId) continue
            box.addView(aboutView(l, soft, width))
        }
        val text = ui.text(m.text, 16f, fg).apply { maxWidth = width }
        if (onPlay == null) box.addView(text) else box.addView(ui.row().apply {
            gravity = Gravity.TOP
            addView(ui.icon(R.drawable.ic_play, "Play", tint = fg, bg = if (mine || ui.p.dark) 0x22FFFFFF else 0x14000000, size = 34) { onPlay() })
            addView(text.apply { maxWidth = width - ui.dp(44); setPadding(ui.dp(10), 0, 0, 0) })
        })
        // A page that a persona made opens in the browser (the web client
        // shows it at /#/page/<id>).
        if (!compact) for (r in m.refs) {
            if (r.kind != "page") continue
            box.addView(ui.button("Open page: ${r.title ?: "page"}", Kind.LINK) {
                startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(settings.server.trimEnd('/') + "/#/page/" + Uri.encode(r.ref))))
            }.apply { gravity = Gravity.START; setPadding(0, ui.dp(6), 0, ui.dp(2)); minHeight = 0 })
        }
        val meta = mutableListOf(if (compact) day(m.createdMs) else ChatTime.clock(m.createdMs.toLong()))
        if (m.visibility == "shared") meta.add("shared")
        box.addView(ui.text(meta.joinToString(" · "), 11f, soft).apply { gravity = Gravity.END; maxWidth = width; setPadding(0, ui.dp(2), 0, 0) },
            LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT))
        if (!compact) box.setOnLongClickListener { audit(m); true }
        return LinearLayout(this).apply {
            gravity = if (mine) Gravity.END else Gravity.START
            addView(box, LinearLayout.LayoutParams(WRAP_CONTENT, WRAP_CONTENT))
        }
    }

    /** A link of a message to an earlier one, as a quote at the top of the bubble. */
    private fun aboutView(l: MessageLink, color: Int, width: Int): TextView {
        val label = l.kind.replace('-', ' ')
        val target = when {
            l.target.kind == "utterance" -> "“${l.snippet ?: "earlier message"}”"
            else -> "${l.target.kind.replace('-', ' ')} ${l.target.title ?: l.target.ref}"
        }
        return ui.text("$label: $target", 12f, color).apply {
            maxWidth = width
            maxLines = 3
            ellipsize = android.text.TextUtils.TruncateAt.END
            background = ui.shape(if (ui.p.dark) 0x1FFFFFFF else 0x14000000, 8)
            setPadding(ui.dp(8), ui.dp(4), ui.dp(8), ui.dp(4))
            layoutParams = LinearLayout.LayoutParams(WRAP_CONTENT, WRAP_CONTENT).apply { bottomMargin = ui.dp(4) }
            if (l.target.kind == "utterance") setOnClickListener { goTo(l.target.ref) }
        }
    }

    /** Shows an earlier message: in place when it is on the page, else with its trace. */
    private fun goTo(id: String) {
        shown[id]?.let { v -> scroll.post { scroll.smoothScrollTo(0, v.top) }; return }
        background(null) {
            val t = client().magi.trace(TraceRequest(message = id, direction = "both", depth = 2UL))
            runOnUiThread {
                val col = ui.column(16)
                t.messages.sortedBy { it.createdMs }.forEach { col.addView(messageView(it, true)) }
                AlertDialog.Builder(this).setTitle("Context").setView(ScrollView(this).apply { addView(col) }).setPositiveButton("Close", null).show()
            }
        }
    }

    private fun show(m: Message, live: Boolean) {
        if (shown.containsKey(m.id)) return
        val audio = m.audio
        // A notice is said by the notice flow (onNotice), not as a reply.
        val spoken = m.authorKind == "persona" && (m.modality == "voice" || m.modality == "notice")
        val play: (() -> Unit)? = if (spoken || audio != null) ({ if (spoken) speakParts(m.id, m.text) else playAudio(audio!!) }) else null
        val ms = m.createdMs.toLong()
        if (lastShownMs == 0L || ChatTime.otherDay(lastShownMs, ms)) {
            messages.addView(ui.text(ChatTime.day(ms), 12f, ui.p.muted).apply {
                background = ui.shape(ui.p.surface, 12)
                setPadding(ui.dp(12), ui.dp(4), ui.dp(12), ui.dp(4))
            }, LinearLayout.LayoutParams(WRAP_CONTENT, WRAP_CONTENT).apply { gravity = Gravity.CENTER_HORIZONTAL; topMargin = ui.dp(12); bottomMargin = ui.dp(2) })
            lastAuthor = null
        }
        // Bubbles of one author in a row are a group. The stream of a persona
        // does not name that persona on each group.
        val author = "${m.authorKind}:${m.author}:${m.persona}"
        val own = m.authorKind == "persona" && m.persona.ifEmpty { m.author } == persona
        val box = messageView(m, named = author != lastAuthor && !own, onPlay = play)
        // Speak a reply to a voice message, or when spoken replies are on.
        if (play != null && live && m.modality == "voice" && (voiceTurn || settings.speakReplies || handsFree.on)) speakParts(m.id, m.text)
        shown[m.id] = box
        messages.addView(box, LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT).apply { topMargin = ui.dp(if (author == lastAuthor) 2 else 10) })
        lastAuthor = author
        lastShownMs = ms
        lastId = m.id
        // A reply links to the message; a notice does not answer one.
        if (m.authorKind == "human") waitingSince = m.createdMs.toLong()
        else if (m.authorKind == "persona" && m.modality != "notice") waitingSince = 0L
        updateWorking()
    }

    /** Shows the working row for 3 minutes after a message of a person, until a reply comes. */
    private fun updateWorking() {
        main.removeCallbacks(hideWorking)
        main.removeCallbacks(pollActivity)
        val now = System.currentTimeMillis()
        val left = if (waitingSince > 0) waitingSince + WORKING_MS - now else 0L
        // A long look up keeps the row while the activity says that Magi works.
        val active = waitingSince > 0 && now - activitySeen < 60_000L
        workingRow?.visibility = if (left > 0 || active) View.VISIBLE else View.GONE
        if (left > 0) main.postDelayed(hideWorking, left)
        if (waitingSince > 0 && now - waitingSince < 30 * 60_000L) main.postDelayed(pollActivity, 2500)
        if (waitingSince == 0L) workingText?.text = "${personaName(persona)} is working on it"
    }

    /** Reads what Magi does now ("checking the calendar") for the working row. */
    private fun readActivity() {
        if (waitingSince == 0L) return
        background(null) {
            val a = client().magi.activity(ActivityRequest(persona = persona))
            runOnUiThread {
                if (a.working) {
                    activitySeen = System.currentTimeMillis()
                    workingText?.text = "${personaName(persona)} is ${a.text ?: "working on it"}"
                }
                updateWorking()
            }
        }
    }

    /** The actions of a message: copy it, and see its links and who can read it, for correction. */
    private fun audit(m: Message) {
        background(null) {
            val links = client().magi.listLinks(ListLinksRequest(message = m.id, all = true)).links
            runOnUiThread {
                val col = ui.column(16)
                lateinit var dialog: AlertDialog
                col.addView(ui.button("Copy the text") {
                    getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("Magi", m.text))
                    dialog.dismiss()
                })
                m.generatedBy?.let { col.addView(ui.text("Made by $it", 13f, ui.p.muted).apply { setPadding(0, ui.dp(12), 0, 0) }) }
                col.addView(ui.text(if (links.isEmpty()) "No links." else "Links", 14f, ui.p.muted).apply { setPadding(0, ui.dp(16), 0, 0) })
                for (l in links) {
                    col.addView(ui.text("${l.kind} → ${l.snippet?.let { "“$it”" } ?: (l.target.title ?: l.target.ref)}", 15f).apply { setPadding(0, ui.dp(10), 0, 0) })
                    col.addView(ui.text("${l.state}, by ${l.author.substringAfter(':')}, ${(l.confidence * 100).toInt()}%", 12f, ui.p.muted).apply { setPadding(0, 0, 0, ui.dp(6)) })
                    if (l.state == "current" || l.state == "tentative") col.addView(ui.button("Not related") {
                        background(null) { client().magi.unlink(UnlinkRequest(link = l.id)) }
                        dialog.dismiss()
                    })
                }
                val p = personas.firstOrNull { it.id == m.persona.ifEmpty { persona } }
                if (p != null && p.sharing != "never") {
                    val other = if (m.visibility == "shared") "private" else "shared"
                    col.addView(ui.text("Visibility: ${m.visibility}" + if (m.visibilityBy.isEmpty()) " (not classified yet)" else " (by ${m.visibilityBy.substringAfter(':')})", 14f, ui.p.muted)
                        .apply { setPadding(0, ui.dp(16), 0, ui.dp(6)) })
                    col.addView(ui.button(if (other == "shared") "Share with the household" else "Make private") {
                        background(null) { client().magi.setVisibility(SetVisibilityRequest(message = m.id, visibility = other)) }
                        dialog.dismiss()
                    })
                }
                dialog = AlertDialog.Builder(this).setView(ScrollView(this).apply { addView(col) }).setPositiveButton("Close", null).show()
            }
        }
    }

    // --- Review -------------------------------------------------------------

    /**
     * The questions of the librarian. It asks only when it is not sure; each
     * answer becomes an example for it and moves the level at which it asks.
     */
    private fun showReviews() {
        val list = ui.column()
        val status = note("Loading...")
        page("Review", "Review", status, list)
        background(status) {
            val reviews = client().magi.listReviews(ListReviewsRequest(limit = 50UL)).reviews
            runOnUiThread {
                status.text = if (reviews.isEmpty()) "Nothing to review. The librarian asks here when it is not sure how a message relates to an earlier one, or who can read it." else ""
                for (r in reviews) list.addView(reviewView(r, list, status))
            }
        }
    }

    private fun reviewView(r: ReviewItem, list: LinearLayout, status: TextView): View {
        val box = ui.card(ui.text(r.question, 16f, bold = true), ui.text("${(r.confidence * 100).toInt()}% sure", 12f, ui.p.muted).apply { setPadding(0, 0, 0, ui.dp(8)) })
        box.addView(messageView(r.message, true))
        r.target?.let {
            box.addView(ui.text("Earlier", 12f, ui.p.muted).apply { setPadding(0, ui.dp(8), 0, ui.dp(4)) })
            box.addView(messageView(it, true))
        }
        val why = ui.field("Why (optional)")
        fun answer(a: String) = background(status) {
            client().magi.answerReview(AnswerReviewRequest(id = r.id, answer = a, note = why.text.toString().ifBlank { null }))
            runOnUiThread { list.removeView(box) }
        }
        val row = ui.row()
        for (c in r.choices) {
            val label = when {
                r.task == "visibility" -> if (c == "shared") "Household" else "Only me"
                c == "unrelated" -> "Not related"
                else -> "Yes"
            }
            row.addView(ui.button(label, if (label == "Yes") Kind.PRIMARY else Kind.TONAL) { answer(c) })
            row.addView(ui.gap(8))
        }
        row.addView(ui.button("Skip", Kind.LINK) { answer("dismiss") })
        box.addView(ui.gap(10))
        box.addView(why)
        box.addView(ui.gap(10))
        box.addView(actions(row))
        return box
    }

    /**
     * Requests that the egress proxy blocked for review. Only admins see
     * them. A decision is a rule for the next such requests. Placeholders
     * stand in for secrets, so a review never shows a secret.
     */
    private fun showNetwork() {
        val list = ui.column()
        val status = note("Loading...")
        page("Network", "Network reviews", status, list)
        background(status) {
            val reviews = client().magi.listEgressReviews(ListEgressReviewsRequest(limit = 100UL)).reviews
            runOnUiThread {
                status.text = if (reviews.isEmpty()) "No open reviews." else "The egress proxy blocked these requests of tasks. Your decision is a rule for the next such requests."
                for (r in reviews) list.addView(netReviewView(r, list, status))
            }
        }
    }

    private fun netReviewView(r: EgressReview, list: LinearLayout, status: TextView): View {
        val host = r.destination.removeSuffix(":443")
        val box = ui.card(
            ui.text(if (r.kind == "destination") r.destination else "${r.method} ${r.url}", 16f, bold = true).apply { setTextIsSelectable(true) },
            ui.text("${r.reason} (${r.by}; ${r.count} times, last ${day(r.lastMs)}; ${r.owner})", 13f, ui.p.muted).apply { setPadding(0, ui.dp(4), 0, 0) },
        )
        if (r.headers.isNotEmpty() || r.body.isNotEmpty()) {
            val body = try {
                Charsets.UTF_8.newDecoder().decode(java.nio.ByteBuffer.wrap(r.body)).toString()
            } catch (e: java.nio.charset.CharacterCodingException) { "(binary body, ${r.body.size} bytes)" }
            box.addView(ui.text((r.headers.map { "${it.name}: ${it.value}" } + listOf("", body)).joinToString("\n").trim(), 12f, ui.p.muted).apply {
                typeface = Typeface.MONOSPACE
                setTextIsSelectable(true)
                background = ui.shape(ui.p.bg, 8)
                setPadding(ui.dp(8), ui.dp(6), ui.dp(8), ui.dp(6))
                layoutParams = LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT).apply { topMargin = ui.dp(8) }
            })
        }
        val why = ui.field("Note (optional)")
        fun decide(d: String) = background(status) {
            val res = client().magi.decideEgressReview(DecideEgressReviewRequest(id = r.id, decision = d, note = why.text.toString().ifBlank { null }))
            runOnUiThread {
                list.removeView(box)
                status.text = res.rule?.let { "Rule: $it." } ?: "Dismissed. The next blocked request opens a review again."
            }
        }
        val choices = if (r.kind == "destination") listOf("Permit" to "allow-host", "Permit at layer 4" to "allow-layer4", "Block" to "block", "Dismiss" to "dismiss")
            else listOf("Allow ${r.method} to $host" to "allow-host", "Allow this path" to "allow-path", "Block" to "block", "Dismiss" to "dismiss")
        val row = ui.row()
        for ((label, d) in choices) {
            val kind = when (d) { "allow-host" -> Kind.PRIMARY; "block" -> Kind.DANGER; "dismiss" -> Kind.LINK; else -> Kind.TONAL }
            if (row.childCount > 0) row.addView(ui.gap(8))
            row.addView(ui.button(label, kind) { decide(d) })
        }
        box.addView(ui.gap(10))
        box.addView(why)
        box.addView(ui.gap(10))
        box.addView(actions(row))
        return box
    }

    // A spoken reply comes in parts while Magi makes them (listen); each
    // part plays after the one before it.
    @Volatile private var speakingId: String? = null
    private var speakingText = ""
    private var speakingDone: ((String) -> Unit)? = null

    /** Plays the parts of a spoken message as they come. onEnd gets the reason when it ends. */
    private fun speakParts(id: String, text: String, onEnd: ((String) -> Unit)? = null) {
        if (speakingId != null) speechEnded("user")
        player.stop()
        player.volume = 1f
        speakingId = id
        speakingText = text
        speakingDone = onEnd
        background(null) {
            var after = 0UL
            while (speakingId == id) {
                val r = client().magi.listen(ListenRequest(message = id, after = after, waitMs = 20000UL))
                for (p in r.parts) {
                    val a = client().magi.getAudio(GetAudioRequest(digest = p.audio))
                    runOnUiThread { if (speakingId == id) player.enqueue(a.body, a.mediaType, p) }
                }
                after += r.parts.size.toULong()
                if (r.done) {
                    if (after == 0UL) runOnUiThread { if (speakingId == id) speechEnded("error") }
                    break
                }
            }
        }
    }

    /** The spoken cursor: the end of the last word that the person heard. */
    private fun heardUntil(): Int {
        val (tag, ms) = player.position() ?: return 0
        val part = tag as? SpeechPart ?: return 0
        var heard = part.start.toInt()
        for (w in part.words) if (w.startMs.toLong() <= ms) heard = maxOf(heard, w.end.toInt())
        return heard
    }

    /** The spoken message ended: played to the end ("done") or stopped. Main thread. */
    private var lastReplyEnd = 0L

    private fun speechEnded(reason: String) {
        val id = speakingId ?: return
        lastReplyEnd = System.currentTimeMillis()
        val heard = if (reason == "done") speakingText.codePointCount(0, speakingText.length) else heardUntil()
        speakingId = null
        val done = speakingDone
        speakingDone = null
        player.volume = 1f
        background(null) {
            client().magi.reportDelivery(ReportDeliveryRequest(message = id, spokenUntil = heard.toULong(), reason = reason,
                stoppedMs = if (reason == "done") null else System.currentTimeMillis().toULong()))
        }
        done?.invoke(reason)
    }

    /** Stops a spoken message when the person (or a notice) takes the turn. */
    private fun interruptSpeech(reason: String = "interrupted") {
        val id = speakingId ?: return
        if (player.playing) {
            speechEnded(reason)
            player.stop()
        } else {
            speechEnded(reason)
        }
        background(null) { client().magi.stopSpeech(StopSpeechRequest(message = id, reason = reason)) }
    }

    // --- Hands-free talk and notices ----------------------------------------

    private val voice = object : HandsFree.Callbacks {
        override fun speaking() = speakingId
        override fun heardUntil(): Int {
            var h = 0
            val l = java.util.concurrent.CountDownLatch(1)
            main.post { h = this@MainActivity.heardUntil(); l.countDown() }
            l.await(200, java.util.concurrent.TimeUnit.MILLISECONDS)
            return h
        }
        override fun duck(level: Float) { main.post { player.volume = level } }
        override fun interrupt() { main.post { interruptSpeech() } }
        override fun check(message: String, wav: ByteArray, heardUntil: Int, final: Boolean): String = try {
            client().magi.checkOverlap(CheckOverlapRequest(message = message, audio = wav, audioType = "audio/wav",
                heardUntil = heardUntil.toULong(), final = final)).decision
        } catch (e: RpcException) {
            if (e.code == 404 || e.code == 501 || e.code == 503) "missing" else "error"
        } catch (e: Exception) { "error" }
        override fun turn(wav: ByteArray) {
            main.post {
                voiceTurn = true
                val status = TextView(this@MainActivity)
                // The answer to a reply in the 8 s after it needs no name.
                val needName = settings.wakeWord && System.currentTimeMillis() - lastReplyEnd > 8_000
                say(SayRequest(persona = persona, audio = wav, audioType = "audio/wav", speakReply = true, wakeWord = if (needName) true else null), status)
            }
        }
        override fun hearing(on: Boolean) {
            hearing = on
            main.post { updateMic() }
        }
    }

    private val noticeQueue = ArrayDeque<Notice>()
    private var noticeSaying = false

    /** A notice while the app is open (Attention.foreground). Any thread. */
    private fun onNotice(n: Notice): String {
        val say = n.urgency != "low"
        main.post {
            addNotice(n)
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
        if (speakingId != null) interruptSpeech("notice")
        val stream = if (player.communication) AudioManager.STREAM_VOICE_CALL else AudioManager.STREAM_MUSIC
        runCatching { ToneGenerator(stream, 70).startTone(if (n.urgency == "high") ToneGenerator.TONE_PROP_BEEP2 else ToneGenerator.TONE_PROP_ACK, 250) }
        main.postDelayed({
            speakParts(n.message, n.text) { reason ->
                noticeSaying = false
                if (reason == "done") { removeNotice(n.id); background(null) { Attention.ack(this, client(), n.id) } }
                pumpNotices()
            }
        }, 350)
    }

    private val noticeViews = HashMap<String, View>()

    private fun addNotice(n: Notice) {
        val bar = noticeBar ?: return
        if (noticeViews.containsKey(n.id)) return
        val body = ui.text(n.text, 14f).apply { maxLines = 2 }
        body.setOnClickListener { body.maxLines = if (body.maxLines == 2) 200 else 2 }
        val box = ui.card(ui.text(personaName(n.persona) + ": " + n.title, 14f, bold = true), body)
        box.background = ui.shape(ui.p.notice, 14)
        box.addView(ui.button("Dismiss", Kind.LINK) { removeNotice(n.id); background(null) { Attention.ack(this@MainActivity, client(), n.id) } }
            .apply { gravity = Gravity.END; minHeight = 0; setPadding(0, ui.dp(6), 0, 0) })
        noticeViews[n.id] = box
        bar.addView(box)
    }

    private fun removeNotice(id: String) {
        noticeViews.remove(id)?.let { (it.parent as? LinearLayout)?.removeView(it) }
    }

    /** Opened from a notification: show the conversation of its persona and acknowledge it. */
    private fun openNotice(intent: Intent?) {
        val id = intent?.getStringExtra(Attention.EXTRA_NOTICE) ?: return
        val p = intent.getStringExtra(Attention.EXTRA_PERSONA)
        intent.removeExtra(Attention.EXTRA_NOTICE)
        if (settings.token == null) return
        background(null) { Attention.ack(this, client(), id) }
        if (p != null && p != persona) showConversation(p)
    }

    private fun playAudio(digest: String) {
        background(null) {
            val a = client().magi.getAudio(GetAudioRequest(digest = digest))
            runOnUiThread { player.play(a.body, a.mediaType) }
        }
    }

    // --- Tasks --------------------------------------------------------------

    private fun showTasks() {
        val list = ui.column()
        val status = note("Loading...")
        page("Tasks", "Tasks", status, list)
        background(status) {
            val tasks = client().magi.listTasks(ListTasksRequest(limit = 30UL)).tasks
            runOnUiThread {
                status.text = if (tasks.isEmpty()) "No tasks yet. Ask Magi to change something." else ""
                for (t in tasks) {
                    val state = when (t.status) { "waiting" -> ui.p.accent; "failed", "blocked", "gave-up" -> ui.p.danger; else -> ui.p.muted }
                    val box = ui.card(
                        ui.text(t.title, 16f, bold = true),
                        ui.text(listOf(t.status, t.currentStep).filter { it.isNotEmpty() }.joinToString(" · "), 13f, state).apply { setPadding(0, ui.dp(2), 0, 0) },
                    )
                    t.error?.let { box.addView(ui.text(it, 13f, ui.p.danger).apply { setPadding(0, ui.dp(4), 0, 0) }) }
                    val row = ui.row()
                    fun add(v: View) { if (row.childCount > 0) row.addView(ui.gap(8)); row.addView(v) }
                    t.waitingFor?.let { prompt ->
                        box.addView(ui.text(prompt, 14f).apply { setPadding(0, ui.dp(8), 0, 0) })
                        for (outcome in t.decisions ?: listOf("approve", "reject")) {
                            add(ui.button(outcome.replaceFirstChar { it.uppercase() }, if (outcome == "approve") Kind.PRIMARY else Kind.TONAL) { decide(t.id, outcome, status) })
                        }
                    }
                    add(ui.button("Diff") { showDiff(t.id, status) })
                    // A shell in the sandbox of the task opens in the
                    // browser (the web client shows it at /#/terminal/<id>).
                    if (t.terminal == true) add(ui.button("Terminal") {
                        startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(settings.server.trimEnd('/') + "/#/terminal/" + Uri.encode(t.id))))
                    })
                    if (t.status == "running" || t.status == "waiting") add(ui.button("Stop", Kind.DANGER) { cancel(t.id, status) })
                    box.addView(ui.gap(10))
                    box.addView(actions(row))
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

    private fun label(text: String) = ui.text(text, 13f, ui.p.muted, bold = true).apply { setPadding(ui.dp(4), ui.dp(14), 0, ui.dp(6)) }

    /** The line of a list page that tells its state or a failure. */
    internal fun note(text: String) = ui.text(text, 14f, ui.p.muted).apply { setPadding(ui.dp(2), 0, ui.dp(2), ui.dp(10)) }

    /** A row of buttons that can be wider than the card. */
    internal fun actions(row: LinearLayout) = HorizontalScrollView(this).apply { isHorizontalScrollBarEnabled = false; addView(row) }

    /** A list page: the title, the views in a scroll, and the tabs. */
    internal fun page(tab: String, title: String, vararg views: View) {
        val col = ui.column(14)
        views.forEach { col.addView(it) }
        // The back button of a list page goes to the conversation.
        back = { showConversation() }
        show(ui.column().apply {
            addView(topBar(title))
            addView(ScrollView(this@MainActivity).apply { addView(col) }, LinearLayout.LayoutParams(MATCH_PARENT, 0, 1f))
            addView(nav(tab))
        })
    }

    /**
     * Shows a page inside the system bars and above the keyboard. Since
     * Android 15, apps draw behind the status bar unless they add this space.
     */
    private fun show(v: View) {
        val frame = FrameLayout(this)
        frame.setBackgroundColor(ui.p.bg)
        frame.addView(v, FrameLayout.LayoutParams(MATCH_PARENT, MATCH_PARENT))
        frame.setOnApplyWindowInsetsListener { view, insets ->
            if (Build.VERSION.SDK_INT >= 30) {
                val b = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime() or WindowInsets.Type.displayCutout())
                view.setPadding(b.left, b.top, b.right, b.bottom)
                // The keyboard takes the place of the tabs.
                navBar?.visibility = if (insets.isVisible(WindowInsets.Type.ime())) View.GONE else View.VISIBLE
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
