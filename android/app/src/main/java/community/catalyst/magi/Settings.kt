package community.catalyst.magi

import android.content.Context

/**
 * Settings and the session, in the private preferences of the app. The token
 * is a 12-hour user token; refresh gives a new one while the login session
 * is active (7 days).
 */
class Settings(context: Context) {
    private val p = context.getSharedPreferences("magi", Context.MODE_PRIVATE)

    var server: String
        get() = p.getString("server", "") ?: ""
        set(v) = p.edit().putString("server", v).apply()
    var identity: String
        get() = p.getString("identity", "") ?: ""
        set(v) = p.edit().putString("identity", v).apply()
    /** The app asked for the notification permission once. */
    var askedNotify: Boolean
        get() = p.getBoolean("asked_notify", false)
        set(v) = p.edit().putBoolean("asked_notify", v).apply()
    /** The app showed how Magi becomes the assistant app of the phone, one time. */
    var askedAssistant: Boolean
        get() = p.getBoolean("asked_assistant", false)
        set(v) = p.edit().putBoolean("asked_assistant", v).apply()
    /**
     * The first turn of a conversation needs the name of a persona ("Hey,
     * Archimedes"). Off: with the app on the screen, Magi takes each turn.
     * With the app in the background, a name is always needed.
     */
    var wakeWord: Boolean
        get() = p.getBoolean("wake_word", true)
        set(v) = p.edit().putBoolean("wake_word", v).apply()
    var speakReplies: Boolean
        get() = p.getBoolean("speak", true)
        set(v) = p.edit().putBoolean("speak", v).apply()

    /**
     * The microphone of hands-free talk as the person left it. The app turns
     * it on again when it starts, and after the phone starts.
     */
    var micOn: Boolean
        get() = p.getBoolean("mic_on", false)
        set(v) = p.edit().putBoolean("mic_on", v).apply()
    /** The tab that the person used last ("Talk", "Tasks", ...), and the persona of the last conversation. */
    var lastTab: String
        get() = p.getString("last_tab", "Talk") ?: "Talk"
        set(v) { if (v != lastTab) p.edit().putString("last_tab", v).apply() }
    var lastPersona: String
        get() = p.getString("last_persona", "persona:magi") ?: "persona:magi"
        set(v) { if (v != lastPersona) p.edit().putString("last_persona", v).apply() }

    /** Why the last update of the app did not install; "" after an update that installed. Magi gets it (put-device). */
    var updateError: String
        get() = p.getString("update_error", "") ?: ""
        set(v) = p.edit().putString("update_error", v).apply()
    /** The build that the app downloaded and that waits for the person, and its version name. */
    var readyBuild: Long
        get() = p.getLong("ready_build", 0)
        set(v) = p.edit().putLong("ready_build", v).apply()
    var readyName: String
        get() = p.getString("ready_name", "") ?: ""
        set(v) = p.edit().putString("ready_name", v).apply()
    /** The build of the install that the person started. */
    var updateBuild: Long
        get() = p.getLong("update_build", 0)
        set(v) = p.edit().putLong("update_build", v).apply()

    /** "dark" (the default), "light", or "system". */
    var theme: String
        get() = p.getString("theme", "dark") ?: "dark"
        set(v) = p.edit().putString("theme", v).apply()

    /** The role of the last visit (get-access): the tabs show the admin views. Magi checks each call. */
    var operator: Boolean
        get() = p.getBoolean("operator", false)
        set(v) = p.edit().putBoolean("operator", v).apply()

    val token: String? get() = p.getString("token", null)?.takeIf { System.currentTimeMillis() < tokenExpiresMs }
    val tokenExpiresMs: Long get() = p.getLong("token_expires", 0)
    var subject: String
        get() = p.getString("subject", "") ?: ""
        private set(v) = p.edit().putString("subject", v).apply()

    fun saveToken(token: String, subject: String, expiresMs: Long) {
        p.edit().putString("token", token).putString("subject", subject).putLong("token_expires", expiresMs).apply()
    }

    fun clearToken() {
        p.edit().remove("token").remove("subject").remove("token_expires").remove("operator").apply()
    }

    /** The login that this app started and waits to finish. Keep login_id secret. */
    var pendingLogin: Pair<String, Long>?
        get() = p.getString("login_id", null)?.let { it to p.getLong("login_expires", 0) }
        set(v) {
            val e = p.edit()
            if (v == null) e.remove("login_id").remove("login_expires") else e.putString("login_id", v.first).putLong("login_expires", v.second)
            e.apply()
        }
}
