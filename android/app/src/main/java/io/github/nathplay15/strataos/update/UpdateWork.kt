package io.github.nathplay15.strataos.update

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.IntentCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import io.github.nathplay15.strataos.BuildConfig
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.notify.Notifier
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** The daily check, as the settings ask: nothing, a notification, or the update itself. */
class UpdateWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        val ctx = applicationContext
        val p = Updater.prefs(ctx)
        if (p.mode == UpdatePrefs.OFF) return@withContext Result.success()
        try {
            Updater.check(ctx)
        } catch (e: UpdateException) {
            return@withContext if (runAttemptCount < 3) Result.retry() else Result.success()
        }
        val r = Updater.available(ctx) ?: return@withContext Result.success()
        val auto = p.mode == UpdatePrefs.AUTO && Updater.canInstall(ctx) && !(p.wifiOnly && Updater.meteredNow(ctx))
        if (auto) {
            try {
                Updater.update(ctx, r)
                return@withContext Result.success() // InstallReceiver tells what happened
            } catch (_: UpdateException) {
                // Left to the user: the notification opens the screen that says what went wrong.
            }
        }
        if (p.notified != r.tag) {
            UpdateNotices.available(ctx, r)
            p.notified = r.tag
        }
        Result.success()
    }
}

object UpdateScheduler {
    private const val WORK = "app-update"

    /** Schedules (or cancels) the daily check after a change of the settings. */
    fun apply(ctx: Context) {
        val p = Updater.prefs(ctx)
        val wm = WorkManager.getInstance(ctx)
        if (p.mode == UpdatePrefs.OFF) {
            wm.cancelUniqueWork(WORK)
            return
        }
        val constraints = Constraints.Builder()
            .setRequiredNetworkType(if (p.wifiOnly) NetworkType.UNMETERED else NetworkType.CONNECTED)
            .setRequiresBatteryNotLow(true)
            .build()
        wm.enqueueUniquePeriodicWork(
            WORK, ExistingPeriodicWorkPolicy.UPDATE,
            PeriodicWorkRequestBuilder<UpdateWorker>(1, TimeUnit.DAYS).setConstraints(constraints).build(),
        )
    }
}

object UpdateNotices {
    private const val ID = 3

    private fun post(ctx: Context, title: String, text: String, action: Boolean) {
        if (!Notifier.canPost(ctx)) return
        val open = Notifier.openApp(ctx, "update")
        val n = NotificationCompat.Builder(ctx, Notifier.UPDATES)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(open)
            .setAutoCancel(true)
            .apply { if (action) addAction(0, ctx.getString(R.string.update_action), open) }
            .build()
        @Suppress("MissingPermission")
        NotificationManagerCompat.from(ctx).notify(ID, n)
    }

    fun available(ctx: Context, r: Release) =
        post(ctx, ctx.getString(R.string.update_available, r.version.toString()), ctx.getString(R.string.update_available_text), action = true)

    fun ready(ctx: Context, version: String) =
        post(ctx, ctx.getString(R.string.update_ready_title, version), ctx.getString(R.string.update_ready_text), action = true)

    fun updated(ctx: Context, version: String) =
        post(ctx, ctx.getString(R.string.update_updated_title, version), ctx.getString(R.string.update_updated_text), action = false)

    fun cancel(ctx: Context) = NotificationManagerCompat.from(ctx).cancel(ID)
}

/** The result of an installation session (PackageInstaller). */
class InstallReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        val msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: ""
        val p = Updater.prefs(ctx)
        when (status) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                // Android wants the user to confirm (first update, or before Android 12).
                val confirm = IntentCompat.getParcelableExtra(intent, Intent.EXTRA_INTENT, Intent::class.java)
                Updater.onStatus(Updater.Step.WAITING_USER)
                if (confirm != null && Updater.visible) {
                    ctx.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                } else {
                    // In the background an activity cannot be started: the notification brings the user to the Install button.
                    Updater.onStatus(Updater.Step.IDLE)
                    UpdateNotices.ready(ctx, intent.getStringExtra(TAG)?.let { AppVersion.parse(it)?.toString() } ?: "")
                }
            }
            PackageInstaller.STATUS_SUCCESS -> Updater.onStatus(Updater.Step.IDLE) // the process usually ends before this
            PackageInstaller.STATUS_FAILURE_ABORTED -> Updater.onStatus(Updater.Step.FAILED, UpdateException(UpdateException.Kind.CANCELLED))
            else -> Updater.onStatus(Updater.Step.FAILED, UpdateException(UpdateException.Kind.INSTALL, msg.ifEmpty { "status $status" }))
        }
    }

    companion object {
        const val TAG = "tag"
    }
}

/** The app was replaced by a new version: tidy up, and say so when it happened in the background. */
class UpdatedReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val p = Updater.prefs(ctx)
        Updater.cleanUp(ctx)
        UpdateNotices.cancel(ctx)
        val v = p.installing
        p.installing = ""
        if (v.isNotEmpty() && v == BuildConfig.VERSION_NAME) UpdateNotices.updated(ctx, v)
        UpdateScheduler.apply(ctx)
    }
}
