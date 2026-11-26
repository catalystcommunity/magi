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
import community.catalyst.magi.client.magi.DeviceHealth
import community.catalyst.magi.client.magi.Notice
import community.catalyst.magi.client.magi.PutDeviceRequest
import community.catalyst.magi.client.magi.WaitNoticesRequest
import java.security.SecureRandom

/**
 * Devices and notices (docs/attention.md). The phone is a device of the
 * person. NoticeService waits for notices. Talk says or shows a notice to
 * a person who is there (the app is on the screen, or a conversation is
 * open); else the service shows it as a system notification. The service
 * also keeps the process for hands-free talk (Talk.kt).
 */
object Attention {
    private const val TAG = "MagiNotices"
    const val EXTRA_NOTICE = "community.catalyst.magi.NOTICE"
    const val EXTRA_PERSONA = "community.catalyst.magi.PERSONA"

    private var lastHealth = 0L

    fun deviceId(context: Context): String {
        val p = context.getSharedPreferences("magi", Context.MODE_PRIVATE)
        p.getString("device_id", null)?.let { return it }
        val b = ByteArray(12).also { SecureRandom().nextBytes(it) }
        val id = "android-" + b.joinToString("") { "%02x".format(it) }
        p.edit().putString("device_id", id).apply()
        return id
    }

    fun hello(context: Context, client: HttpCarrier) {
        // The version tells Magi which phones need an update, and that an
        // update installed. The failure is "" when the last update installed.
        val info = context.packageManager.getPackageInfo(context.packageName, 0)
        client.magi.putDevice(PutDeviceRequest(id = deviceId(context), kind = "phone", name = Build.MODEL.take(80).ifEmpty { "Android phone" },
            can = listOf("speak", "notify", "show"), active = Talk.view != null, handsFree = Talk.on,
            appVersion = info.versionName?.take(80), appBuild = info.longVersionCode.toULong(), updateError = Settings(context).updateError,
            health = health(context)))
    }

    /**
     * The health of the phone for Magi: the battery, and what the
     * microphone did since the report before. With it Magi finds a build
     * that uses too much battery. At most one report in 5 minutes has the
     * counters; the calls between have none.
     */
    @Synchronized private fun health(context: Context): DeviceHealth? {
        val now = System.currentTimeMillis()
        if (lastHealth != 0L && now - lastHealth < 5 * 60_000) return null
        val window = if (lastHealth == 0L) 0L else (now - lastHealth) / 1000
        lastHealth = now
        val b = context.registerReceiver(null, android.content.IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = b?.getIntExtra(android.os.BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = b?.getIntExtra(android.os.BatteryManager.EXTRA_SCALE, -1) ?: -1
        val status = b?.getIntExtra(android.os.BatteryManager.EXTRA_STATUS, -1) ?: -1
        val c = Talk.counters
        return DeviceHealth(
            battery = if (level >= 0 && scale > 0) (level * 100 / scale).toULong() else null,
            charging = if (status < 0) null else status == android.os.BatteryManager.BATTERY_STATUS_CHARGING || status == android.os.BatteryManager.BATTERY_STATUS_FULL,
            windowS = window.toULong(),
            listenS = c?.listenMs?.getAndSet(0)?.let { (it / 1000).toULong() },
            soundS = c?.soundMs?.getAndSet(0)?.let { (it / 1000).toULong() },
            wakes = c?.wakes?.getAndSet(0)?.toULong(),
            turns = c?.turns?.getAndSet(0)?.toULong(),
            dropped = c?.dropped?.getAndSet(0)?.toULong())
    }

    const val EXTRA_MIC = "community.catalyst.magi.MIC"
    const val EXTRA_RESUME = "community.catalyst.magi.RESUME"

    /**
     * Starts the notice service. mic: hands-free talk is on; then the
     * service also has the microphone type, so that Android lets the app
     * use the microphone with the app in the background and the screen
     * off. Android permits that type for an app on the screen and for the
     * assistant app of the phone; for another app in the background the
     * service tells Talk that it has no microphone (Talk.micRefused).
     * resume: the phone or the app started; Talk turns the microphone on
     * when the person left it on.
     */
    fun start(context: Context, mic: Boolean = Talk.on, resume: Boolean = false) {
        val i = Intent(context, NoticeService::class.java).putExtra(EXTRA_MIC, mic).putExtra(EXTRA_RESUME, resume)
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

    override fun onCreate() {
        super.onCreate()
        Talk.init(this)
    }

    private fun note(text: String): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, "service")
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle("Magi")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .build()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        Attention.channels(getSystemService(NotificationManager::class.java))
        // A start for another reason (the phone started, a page opened) keeps
        // the microphone type while the microphone is on.
        val mic = intent?.getBooleanExtra(Attention.EXTRA_MIC, false) == true || Talk.on
        var type = 0
        if (Build.VERSION.SDK_INT >= 34) type = ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING
        var refused = false
        if (mic && Build.VERSION.SDK_INT >= 30) {
            // Android refuses the microphone type to an app that is not on
            // the screen and is not the assistant app (a SecurityException).
            try {
                startForeground(1, note("Listening for a name"), type or ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE)
            } catch (e: Exception) {
                Log.w("MagiNotices", "no microphone for the service: $e")
                refused = true
            }
        }
        if (!mic || refused || Build.VERSION.SDK_INT < 30) {
            val text = if (refused) "Open Magi to turn the microphone on" else "Ready for notices"
            try {
                if (type != 0) startForeground(1, note(text), type) else startForeground(1, note(text))
            } catch (e: Exception) {
                // Android did not permit a foreground service now (the app is
                // in the background). The next open of the app starts it.
                Log.w("MagiNotices", "no foreground service: $e")
                stopSelf()
                return START_NOT_STICKY
            }
        }
        if (refused) Talk.micRefused()
        if (!running) {
            running = true
            Thread({ loop() }, "magi-notices").start()
        }
        // Android started the service again after it stopped the app
        // (intent is null), or the phone started: the microphone as the
        // person left it.
        if (intent == null || intent.getBooleanExtra(Attention.EXTRA_RESUME, false)) Talk.resume()
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
        // The build that this loop asked for: one download for each build.
        var triedBuild = 0L
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
                // A notice of a new build wakes this call: download it now, not
                // at the next hour, and tell the person that it is ready.
                val newest = r.appBuild?.toLong() ?: 0L
                if (newest > Updater.installedVersion(this) && newest != triedBuild) {
                    triedBuild = newest
                    val result = runCatching { Updater.check(applicationContext) }
                    Log.i("MagiUpdater", "check after a notice: ${result.getOrElse { it.toString() }}")
                }
                if (r.closed.isNotEmpty()) {
                    val nm = getSystemService(NotificationManager::class.java)
                    r.closed.forEach { nm.cancel(it, 0) }
                    Talk.view?.let { v -> android.os.Handler(android.os.Looper.getMainLooper()).post { r.closed.forEach { v.noticeClosed(it) } } }
                }
                for (n in r.notices) {
                    val how = Talk.notice(n) ?: run { Attention.notify(this, n); "notified" }
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
        if (intent.action == Intent.ACTION_BOOT_COMPLETED && Settings(context).token != null) {
            Talk.init(context)
            Attention.start(context, mic = false, resume = true)
        }
    }
}
