package community.catalyst.magi

import android.app.Activity
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.job.JobInfo
import android.app.job.JobParameters
import android.app.job.JobScheduler
import android.app.job.JobService
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import android.util.Log
import community.catalyst.magi.client.identity.RefreshRequest
import community.catalyst.magi.client.magi.GetAppPackageRequest
import community.catalyst.magi.client.magi.GetAppReleaseRequest
import java.io.File
import java.security.MessageDigest

/**
 * Self-update from Magi. Magi keeps the newest app build (put-app-release).
 * A periodic job, each start of the app, and a notice of a new build ask
 * for it. A build with a higher version code is downloaded and checked
 * against its SHA-256 digest. The app then tells the person that an update
 * is ready. It installs the build only when the person says so: the phone
 * can ask for a confirmation or show a warning (Google Play Protect) at an
 * install, and the person must expect that.
 */
object Updater {
    private const val TAG = "MagiUpdater"
    private const val JOB_ID = 4201
    private const val CHANNEL = "updates"
    private const val NOTIFICATION = 4202
    const val ACTION_STATUS = "community.catalyst.magi.UPDATE_STATUS"
    /** The person opened the notification of a ready update. */
    const val EXTRA_INSTALL = "community.catalyst.magi.INSTALL_UPDATE"

    /** The activity in the foreground, which can show a confirmation. */
    @Volatile var foreground: Activity? = null

    /** A downloaded build that waits for the person. */
    data class Ready(val code: Long, val name: String)

    /** Schedules the periodic check (about once an hour, on any network). */
    fun schedule(context: Context) {
        val js = context.getSystemService(JobScheduler::class.java)
        if (js.getPendingJob(JOB_ID) != null) return
        val job = JobInfo.Builder(JOB_ID, ComponentName(context, UpdateJob::class.java))
            .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
            .setPeriodic(60 * 60_000L)
            .setPersisted(true)
            .build()
        js.schedule(job)
    }

    private fun file(context: Context) = File(context.filesDir, "update.apk")

    /** The downloaded build that is newer than the app, or null. */
    fun ready(context: Context): Ready? {
        val settings = Settings(context)
        val code = settings.readyBuild
        if (code <= installedVersion(context) || !file(context).isFile) return null
        return Ready(code, settings.readyName)
    }

    /**
     * Asks Magi for a newer build and downloads it. It installs nothing.
     * The result is "up to date", "ready" (a build waits for the person),
     * "no release", or "not signed in". A build that is new to the phone
     * gives a system notification. Call it off the main thread.
     */
    @Synchronized
    fun check(context: Context): String {
        val settings = Settings(context)
        if (settings.server.isEmpty() || settings.token == null) return "not signed in"
        val rpc = HttpCarrier(settings.server) { settings.token }
        if (settings.tokenExpiresMs - System.currentTimeMillis() < 2 * 3600_000) {
            runCatching { rpc.identity.refresh(RefreshRequest()) }
                .onSuccess { settings.saveToken(it.token, it.subject, it.expiresMs.toLong()) }
        }
        val rel = rpc.magi.getAppRelease(GetAppReleaseRequest()).release ?: return "no release"
        val installed = installedVersion(context)
        val code = rel.versionCode.toLong()
        if (code <= installed) {
            clear(context)
            return "up to date"
        }
        if (ready(context)?.code == code) return "ready"
        val apk = rpc.magi.getAppPackage(GetAppPackageRequest(rel.digest)).body
        val sum = "sha256:" + MessageDigest.getInstance("SHA-256").digest(apk).joinToString("") { "%02x".format(it) }
        if (sum != rel.digest) throw IllegalStateException("the app build digest is $sum, not ${rel.digest}")
        val tmp = File(context.filesDir, "update.apk.part")
        tmp.writeBytes(apk)
        if (!tmp.renameTo(file(context))) throw IllegalStateException("the app could not keep the download")
        settings.readyBuild = code
        settings.readyName = rel.versionName
        Log.i(TAG, "downloaded ${rel.versionName} ($code); the app runs $installed")
        notifyReady(context, Ready(code, rel.versionName))
        return "ready"
    }

    /** Tells the person that an update waits. A touch opens the app with the question. */
    private fun notifyReady(context: Context, r: Ready) {
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "App updates", NotificationManager.IMPORTANCE_DEFAULT))
        val open = Intent(context, MainActivity::class.java).putExtra(EXTRA_INSTALL, true)
        val pi = PendingIntent.getActivity(context, NOTIFICATION, open, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        nm.notify(NOTIFICATION, Notification.Builder(context, CHANNEL)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentTitle("Magi update is ready")
            .setContentText("Touch to install build ${r.name}.")
            .setContentIntent(pi)
            .setAutoCancel(true)
            .build())
    }

    /** Installs the downloaded build. The person asked for it. */
    fun install(context: Context): String {
        val r = ready(context) ?: return "no update is ready"
        val settings = Settings(context)
        settings.updateBuild = r.code
        settings.updateError = ""
        commit(context, file(context).readBytes())
        Log.i(TAG, "installing ${r.name} (${r.code}) over ${installedVersion(context)}")
        return "installing ${r.name}"
    }

    /** Records why an update did not install. The next put-device tells Magi. The download stays for a new try. */
    fun failed(context: Context, reason: String) {
        Settings(context).updateError = reason.take(400)
        Log.w(TAG, "update failed: $reason")
    }

    /** After an update that installed: no failure, no download, no notification. */
    fun settled(context: Context) {
        val settings = Settings(context)
        if (settings.updateBuild > 0 && installedVersion(context) >= settings.updateBuild) {
            settings.updateError = ""
            settings.updateBuild = 0
        }
        if (settings.readyBuild > 0 && installedVersion(context) >= settings.readyBuild) clear(context)
    }

    private fun clear(context: Context) {
        file(context).delete()
        Settings(context).readyBuild = 0
        context.getSystemService(NotificationManager::class.java).cancel(NOTIFICATION)
    }

    fun installedVersion(context: Context): Long =
        context.packageManager.getPackageInfo(context.packageName, 0).longVersionCode

    private fun commit(context: Context, apk: ByteArray) {
        val pi = context.packageManager.packageInstaller
        // A session that waits for a confirmation stays open. Android permits
        // a fixed number of open sessions for each app, and then refuses a
        // new one ("Too many active sessions"). Only the newest build counts.
        pi.mySessions.forEach { old -> runCatching { pi.abandonSession(old.sessionId) } }
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(context.packageName)
        if (Build.VERSION.SDK_INT >= 31) params.setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        val id = pi.createSession(params)
        pi.openSession(id).use { s ->
            s.openWrite("magi.apk", 0, apk.size.toLong()).use { it.write(apk); s.fsync(it) }
            val status = Intent(context, UpdateReceiver::class.java).setAction(ACTION_STATUS)
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
            s.commit(PendingIntent.getBroadcast(context, id, status, flags).intentSender)
        }
    }
}

/** The periodic update check. */
class UpdateJob : JobService() {
    override fun onStartJob(params: JobParameters): Boolean {
        Thread {
            val result = runCatching { Updater.check(applicationContext) }
            Log.i("MagiUpdater", "check: ${result.getOrElse { it.toString() }}")
            jobFinished(params, false)
        }.start()
        return true
    }

    override fun onStopJob(params: JobParameters) = true
}

/** Receives the result of an install session. */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                // Android asks the person. The person started the install, so the app is open.
                val confirm = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                    else @Suppress("DEPRECATION") intent.getParcelableExtra(Intent.EXTRA_INTENT)
                val activity = Updater.foreground
                if (confirm != null && activity != null) activity.runOnUiThread { activity.startActivity(confirm) }
                else Updater.failed(context, "Android asked for a confirmation of the update, and the app was not open.")
            }
            PackageInstaller.STATUS_SUCCESS -> Log.i("MagiUpdater", "update installed")
            else -> Updater.failed(context, "Android did not install the update (status $status): ${intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "no reason"}")
        }
    }
}
