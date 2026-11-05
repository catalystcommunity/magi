package community.catalyst.magi

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.util.Log
import community.catalyst.magi.client.identity.RefreshRequest
import community.catalyst.magi.client.magi.AckNoticeRequest
import community.catalyst.magi.client.magi.DeliverNoticeRequest
import community.catalyst.magi.client.magi.Notice
import community.catalyst.magi.client.magi.PutDeviceRequest
import community.catalyst.magi.client.magi.WaitNoticesRequest
import java.security.SecureRandom

/**
 * Devices and notices (docs/attention.md). The phone is a device of the
 * person. NoticeService waits for notices while the app is not open and
 * shows them as system notifications; while the app is open, the activity
 * gets them (Attention.foreground) and says or shows them.
 */
object Attention {
    private const val TAG = "MagiNotices"
    const val EXTRA_NOTICE = "community.catalyst.magi.NOTICE"
    const val EXTRA_PERSONA = "community.catalyst.magi.PERSONA"

    /** The activity takes notices while it is open; it returns how it delivered one, or null. */
    @Volatile var foreground: ((Notice) -> String?)? = null
    /** The activity removes notices that another device closed. */
    @Volatile var closed: ((List<String>) -> Unit)? = null
    @Volatile var handsFree = false

    fun deviceId(context: Context): String {
        val p = context.getSharedPreferences("magi", Context.MODE_PRIVATE)
        p.getString("device_id", null)?.let { return it }
        val b = ByteArray(12).also { SecureRandom().nextBytes(it) }
        val id = "android-" + b.joinToString("") { "%02x".format(it) }
        p.edit().putString("device_id", id).apply()
        return id
    }

    fun hello(context: Context, client: HttpCarrier) {
        client.magi.putDevice(PutDeviceRequest(id = deviceId(context), kind = "phone", name = Build.MODEL.take(80).ifEmpty { "Android phone" },
            can = listOf("speak", "notify", "show"), active = foreground != null, handsFree = handsFree))
    }

    const val EXTRA_MIC = "community.catalyst.magi.MIC"

    /**
     * Starts the notice service. mic: hands-free talk is on; then the
     * service also has the microphone type, so that Android lets the app
     * use the microphone with the screen off. Call it with mic true only
     * while the app is in the foreground.
     */
    fun start(context: Context, mic: Boolean = handsFree) {
        val i = Intent(context, NoticeService::class.java).putExtra(EXTRA_MIC, mic)
        runCatching { context.startForegroundService(i) }.onFailure { Log.w(TAG, "start: $it") }
    }

    fun ack(context: Context, client: HttpCarrier, id: String) {
        runCatching { client.magi.ackNotice(AckNoticeRequest(id = id, device = deviceId(context))) }
        context.getSystemService(NotificationManager::class.java).cancel(id, 0)
    }

    internal fun channels(nm: NotificationManager) {
        nm.createNotificationChannel(NotificationChannel("notices-high", "Urgent notices", NotificationManager.IMPORTANCE_HIGH))
        nm.createNotificationChannel(NotificationChannel("notices", "Notices", NotificationManager.IMPORTANCE_DEFAULT))
        nm.createNotificationChannel(NotificationChannel("notices-low", "Quiet notices", NotificationManager.IMPORTANCE_LOW))
        nm.createNotificationChannel(NotificationChannel("service", "Ready for notices", NotificationManager.IMPORTANCE_MIN))
    }

    /** Shows a notice as a system notification. Tap opens the conversation; swipe acknowledges. */
    internal fun notify(context: Context, n: Notice) {
        val nm = context.getSystemService(NotificationManager::class.java)
        val open = Intent(context, MainActivity::class.java).putExtra(EXTRA_NOTICE, n.id).putExtra(EXTRA_PERSONA, n.persona)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val code = n.id.hashCode()
        val tap = PendingIntent.getActivity(context, code, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val swipe = PendingIntent.getBroadcast(context, code, Intent(context, NoticeAckReceiver::class.java).putExtra(EXTRA_NOTICE, n.id),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val channel = when (n.urgency) { "high" -> "notices-high"; "low" -> "notices-low"; else -> "notices" }
        val note = Notification.Builder(context, channel)
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle(n.title)
            .setContentText(n.text.take(200))
            .setStyle(Notification.BigTextStyle().bigText(n.text.take(4000)))
            .setContentIntent(tap)
            .setDeleteIntent(swipe)
            .setAutoCancel(true)
            .setWhen(n.createdMs.toLong())
            .setShowWhen(true)
            .build()
        nm.notify(n.id, 0, note)
    }
}

/**
 * Waits for notices (a long poll that connects out to Magi) for as long as
 * the person is signed in. A foreground service of type remoteMessaging,
 * with a quiet notification, so that Android lets it run.
 */
class NoticeService : Service() {
    @Volatile private var running = false

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val nm = getSystemService(NotificationManager::class.java)
        Attention.channels(nm)
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val mic = intent?.getBooleanExtra(Attention.EXTRA_MIC, false) == true
        val note = Notification.Builder(this, "service")
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle("Magi")
            .setContentText(if (mic) "Hands-free: listening" else "Ready for notices")
            .setContentIntent(open)
            .setOngoing(true)
            .build()
        var type = 0
        if (Build.VERSION.SDK_INT >= 34) type = ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING
        if (mic && Build.VERSION.SDK_INT >= 30) type = type or ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE
        if (type != 0) startForeground(1, note, type) else startForeground(1, note)
        if (!running) {
            running = true
            Thread({ loop() }, "magi-notices").start()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        super.onDestroy()
    }

    private fun loop() {
        val settings = Settings(this)
        var closedAfter = 0UL
        var lastHello = 0L
        while (running) {
            val token = settings.token
            if (token == null) {
                // Signed out, or the session ended: stop until the app starts again.
                stopSelf()
                return
            }
            val client = HttpCarrier(settings.server) { settings.token }
            try {
                if (settings.tokenExpiresMs - System.currentTimeMillis() < 2 * 3600_000) {
                    val t = client.identity.refresh(RefreshRequest())
                    settings.saveToken(t.token, t.subject, t.expiresMs.toLong())
                }
                if (System.currentTimeMillis() - lastHello > 60_000) {
                    Attention.hello(this, client)
                    lastHello = System.currentTimeMillis()
                }
                val dev = Attention.deviceId(this)
                val r = client.magi.waitNotices(WaitNoticesRequest(device = dev, waitMs = 25_000UL, closedAfter = closedAfter))
                closedAfter = r.nowMs
                if (r.closed.isNotEmpty()) {
                    val nm = getSystemService(NotificationManager::class.java)
                    r.closed.forEach { nm.cancel(it, 0) }
                    Attention.closed?.invoke(r.closed)
                }
                for (n in r.notices) {
                    val how = Attention.foreground?.invoke(n) ?: run { Attention.notify(this, n); "notified" }
                    runCatching { client.magi.deliverNotice(DeliverNoticeRequest(id = n.id, device = dev, how = how)) }
                }
            } catch (e: Exception) {
                Log.w("MagiNotices", "wait: $e")
                if (e is RpcException && e.code == 401) { stopSelf(); return }
                if (e is RpcException && e.code == 404) lastHello = 0 // the device is not known: say hello again
                Thread.sleep(5_000)
            }
        }
    }
}

/** A swiped notification: the person saw it. */
class NoticeAckReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val id = intent.getStringExtra(Attention.EXTRA_NOTICE) ?: return
        val pending = goAsync()
        Thread {
            try {
                val s = Settings(context)
                if (s.token != null) Attention.ack(context, HttpCarrier(s.server) { s.token }, id)
            } finally {
                pending.finish()
            }
        }.start()
    }
}

/** Starts the notice service after the phone starts. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED && Settings(context).token != null) Attention.start(context)
    }
}
