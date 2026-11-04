package community.catalyst.magi

import android.app.Activity
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
import java.security.MessageDigest

/**
 * Self-update from Magi. Magi keeps the newest app build (put-app-release).
 * A periodic job and each start of the app ask for it; a build with a
 * higher version code is downloaded, checked against its SHA-256 digest,
 * and installed with PackageInstaller.
 *
 * Android asks the person to confirm the first update. After that this app
 * is the installer of record of itself, and updates install without a
 * question (USER_ACTION_NOT_REQUIRED). A confirmation that Android asks for
 * while the app is in the background waits until the app is open.
 */
object Updater {
    private const val TAG = "MagiUpdater"
    private const val JOB_ID = 4201
    const val ACTION_STATUS = "community.catalyst.magi.UPDATE_STATUS"

    /** The activity in the foreground, which can show a confirmation. */
    @Volatile var foreground: Activity? = null

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

    /** Checks for a newer build and installs it. Call it off the main thread. */
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
        if (rel.versionCode.toLong() <= installed) return "up to date ($installed)"
        val apk = rpc.magi.getAppPackage(GetAppPackageRequest(rel.digest)).body
        val sum = "sha256:" + MessageDigest.getInstance("SHA-256").digest(apk).joinToString("") { "%02x".format(it) }
        if (sum != rel.digest) throw IllegalStateException("the app build digest is $sum, not ${rel.digest}")
        install(context, apk)
        Log.i(TAG, "installing ${rel.versionName} (${rel.versionCode}) over $installed")
        return "installing ${rel.versionName}"
    }

    fun installedVersion(context: Context): Long =
        context.packageManager.getPackageInfo(context.packageName, 0).longVersionCode

    private fun install(context: Context, apk: ByteArray) {
        val pi = context.packageManager.packageInstaller
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
                // The first update: Android asks the person. Only an open app
                // can show the question; otherwise the next start asks again.
                val confirm = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                    else @Suppress("DEPRECATION") intent.getParcelableExtra(Intent.EXTRA_INTENT)
                val activity = Updater.foreground
                if (confirm != null && activity != null) activity.runOnUiThread { activity.startActivity(confirm) }
                else Log.i("MagiUpdater", "the update waits for the app to be open")
            }
            PackageInstaller.STATUS_SUCCESS -> Log.i("MagiUpdater", "update installed")
            else -> Log.w("MagiUpdater", "update failed ($status): ${intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)}")
        }
    }
}
