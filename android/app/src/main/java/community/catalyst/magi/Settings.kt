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
    var speakReplies: Boolean
        get() = p.getBoolean("speak", true)
        set(v) = p.edit().putBoolean("speak", v).apply()

    val token: String? get() = p.getString("token", null)?.takeIf { System.currentTimeMillis() < tokenExpiresMs }
    val tokenExpiresMs: Long get() = p.getLong("token_expires", 0)
    var subject: String
        get() = p.getString("subject", "") ?: ""
        private set(v) = p.edit().putString("subject", v).apply()

    fun saveToken(token: String, subject: String, expiresMs: Long) {
        p.edit().putString("token", token).putString("subject", subject).putLong("token_expires", expiresMs).apply()
    }

    fun clearToken() {
        p.edit().remove("token").remove("subject").remove("token_expires").apply()
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
