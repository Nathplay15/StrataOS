package io.github.nathplay15.strataos.sync

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import androidx.work.workDataOf
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.notify.Notifier
import java.io.File
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/** Opens the phone side of a pair (null: the folder is gone or no longer allowed). */
fun localFsOf(ctx: Context, p: SyncPair): LocalFs? {
    if (p.local.startsWith(APP_PREFIX)) {
        val d = appFolder(ctx, p.local.removePrefix(APP_PREFIX))
        d.mkdirs()
        return FileFs(d).takeIf { d.isDirectory }
    }
    val uri = Uri.parse(p.local)
    val allowed = ctx.contentResolver.persistedUriPermissions.any { it.uri == uri && it.isReadPermission && it.isWritePermission }
    return if (allowed) SafFs(ctx.contentResolver, uri) else null
}

const val APP_PREFIX = "app:"

/** The folders of the app that pairs may use (visible over USB in Android/data). */
fun appFolder(ctx: Context, name: String): File = File(ctx.getExternalFilesDir(null) ?: ctx.filesDir, "Sync/$name")

/**
 * Runs the rounds of the pairs: every 15 minutes at best (WorkManager), when
 * the NAS reports a change while the app is open, and on "Sync now". Each pair
 * has its own conditions (Wi-Fi, charging); scheduled runs wait while battery
 * saver is on. A notification shows while files are transferred.
 */
class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        val manual = inputData.getBoolean("manual", false)
        val only = inputData.getString("pair")
        val power = applicationContext.getSystemService(PowerManager::class.java)
        if (!manual && power?.isPowerSaveMode == true) return@withContext Result.success()
        val api = Graph.api() ?: return@withContext Result.failure()
        val db = Graph.syncDb
        var retry = false
        lock.withLock {
            for (p in db.pairs()) {
                if (only != null && p.id != only) continue
                if (p.paused) {
                    db.updateStatus(p.id) { it.copy(state = "paused", pending = 0, current = "") }
                    continue
                }
                if (isStopped) return@withLock
                when (runPair(p, ApiRemote(api), db)) {
                    Outcome.SignedOut -> {
                        Notifier.signedOut(applicationContext)
                        return@withContext Result.failure()
                    }
                    Outcome.Retry -> retry = true
                    Outcome.Done -> {}
                }
            }
        }
        if (retry && runAttemptCount < 3) Result.retry() else Result.success()
    }

    private enum class Outcome { Done, Retry, SignedOut }

    private fun runPair(p: SyncPair, remote: Remote, db: SyncDb): Outcome {
        val ctx = applicationContext
        val fs = localFsOf(ctx, p)
        if (fs == null) {
            db.updateStatus(p.id) { it.copy(state = "error", error = ctx.getString(R.string.sync_folder_missing), pending = 0, current = "") }
            return Outcome.Done
        }
        val before = db.status(p.id)
        db.updateStatus(p.id) { it.copy(state = "syncing", error = "", pending = 0, done = 0, current = "", decision = "") }
        var allow = before.decision == "apply"
        var restore = before.decision == "restore"
        val device = Graph.prefs.server?.deviceName?.ifEmpty { null } ?: Graph.deviceModel
        val conflicts = ArrayList<SyncEvent>()
        var foreground = false
        var lastWrite = 0L
        var result: RoundResult? = null
        for (i in 0 until 5) {
            val ps = PairSync(remote, db, SyncOptions(device), p, fs, stopped = { isStopped },
                onEvent = { e -> if (e.kind == "conflict") conflicts += e },
                onProgress = { pr ->
                    if (pr.pending > 0 && !foreground) {
                        foreground = true
                        showForeground(p, pr)
                    }
                    val now = System.currentTimeMillis()
                    if (now - lastWrite > 700 || pr.done == pr.pending) {
                        lastWrite = now
                        db.updateStatus(p.id) { it.copy(pending = pr.pending, done = pr.done, current = pr.current, fraction = pr.fraction) }
                        if (foreground) showForeground(p, pr)
                    }
                })
            result = ps.round(allow, restore)
            allow = false
            restore = false
            if (result.error != null || !result.again) break
        }
        val r = result!!
        for (e in conflicts) db.addConflict(p.id, ConflictRecord(e.path, e.msg, System.currentTimeMillis()))
        if (conflicts.isNotEmpty()) notifyConflicts(p, conflicts.size)
        val err = r.error
        val s = db.updateStatus(p.id) { s ->
            val base = s.copy(pending = 0, done = 0, current = "", fraction = 0f, files = r.files, skipped = r.skipped, links = r.links)
            when {
                err is StoppedException -> base.copy(state = "idle")
                err != null -> base.copy(state = "error", error = errorMessage(err))
                r.brake != null -> base.copy(state = "brake", brake = r.brake, lastSync = System.currentTimeMillis())
                else -> base.copy(state = "idle", brake = null, lastSync = System.currentTimeMillis())
            }
        }
        if (s.brake != null && before.brake == null) notifyBrake(p, s.brake)
        if (s.brake == null) NotificationManagerCompat.from(ctx).cancel(TAG_BRAKE, p.id.hashCode())
        return when (err) {
            null -> Outcome.Done
            is UnauthorizedException -> Outcome.SignedOut
            is StoppedException -> Outcome.Retry
            is ApiException -> if (err.status >= 500 || err.status == 429) Outcome.Retry else Outcome.Done
            is java.io.IOException -> if (err.cause is ApiException || err is NotFoundException || err is LocalMissingException) Outcome.Done else Outcome.Retry
            else -> Outcome.Done
        }
    }

    private fun errorMessage(e: Throwable): String = when (e) {
        is LocalMissingException -> applicationContext.getString(R.string.sync_folder_missing)
        is NotFoundException -> applicationContext.getString(R.string.sync_remote_missing)
        else -> io.github.nathplay15.strataos.ui.errorText(applicationContext, e)
    }

    private fun showForeground(p: SyncPair, pr: Progress) {
        val ctx = applicationContext
        val n = NotificationCompat.Builder(ctx, Notifier.SYNC)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(ctx.getString(R.string.sync_running, labelOf(p)))
            .setContentText(ctx.getString(R.string.sync_progress, pr.done, pr.pending, pr.current.substringAfterLast('/')))
            .setOnlyAlertOnce(true)
            .setOngoing(true)
            .setProgress(pr.pending, pr.done, pr.pending == 0)
            .setContentIntent(Notifier.openApp(ctx, "sync"))
            .build()
        runCatching {
            setForegroundAsync(if (Build.VERSION.SDK_INT >= 29) ForegroundInfo(SYNC_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC) else ForegroundInfo(SYNC_ID, n))
        }
    }

    private fun notifyBrake(p: SyncPair, b: Brake) {
        val ctx = applicationContext
        if (!Notifier.canPost(ctx)) return
        fun action(a: String) = PendingIntent.getBroadcast(ctx, (p.id + a).hashCode(),
            Intent(ctx, SyncActionReceiver::class.java).setAction(ACTION_BRAKE).putExtra("pair", p.id).putExtra("answer", a),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val text = ctx.getString(R.string.sync_brake_text, labelOf(p), b.localDeletes, b.remoteDeletes)
        val n = NotificationCompat.Builder(ctx, Notifier.ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(ctx.getString(R.string.sync_brake_title))
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(Notifier.openApp(ctx, "sync"))
            .addAction(0, ctx.getString(R.string.sync_brake_apply), action("apply"))
            .addAction(0, ctx.getString(R.string.sync_brake_restore), action("restore"))
            .setAutoCancel(true)
            .build()
        @Suppress("MissingPermission")
        NotificationManagerCompat.from(ctx).notify(TAG_BRAKE, p.id.hashCode(), n)
    }

    private fun notifyConflicts(p: SyncPair, count: Int) {
        val ctx = applicationContext
        if (!Notifier.canPost(ctx)) return
        val text = ctx.resources.getQuantityString(R.plurals.sync_conflicts_text, count, labelOf(p), count)
        val n = NotificationCompat.Builder(ctx, Notifier.INFO)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(ctx.getString(R.string.sync_conflicts_title))
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(Notifier.openApp(ctx, "sync"))
            .setAutoCancel(true)
            .build()
        @Suppress("MissingPermission")
        NotificationManagerCompat.from(ctx).notify(TAG_CONFLICT, p.id.hashCode(), n)
    }

    companion object {
        private val lock = Mutex()
        const val SYNC_ID = 5
        const val TAG_BRAKE = "sync-brake"
        const val TAG_CONFLICT = "sync-conflict"
        const val ACTION_BRAKE = "io.github.nathplay15.strataos.SYNC_BRAKE"

        fun labelOf(p: SyncPair) = p.remote.removePrefix("/srv/strata").trimStart('/').ifEmpty { p.remote }
    }
}

/** The answers of the deletion brake notification. */
class SyncActionReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != SyncWorker.ACTION_BRAKE) return
        val id = intent.getStringExtra("pair") ?: return
        val answer = intent.getStringExtra("answer")?.takeIf { it == "apply" || it == "restore" } ?: return
        val pending = goAsync()
        Thread {
            try {
                SyncScheduler.resolveBrake(ctx, id, answer)
                NotificationManagerCompat.from(ctx).cancel(SyncWorker.TAG_BRAKE, id.hashCode())
            } finally {
                pending.finish()
            }
        }.start()
    }
}

object SyncScheduler {
    private fun constraints(p: SyncPair, manual: Boolean = false) = Constraints.Builder()
        .setRequiredNetworkType(if (p.wifiOnly && !manual) NetworkType.UNMETERED else NetworkType.CONNECTED)
        .setRequiresCharging(p.chargingOnly && !manual)
        .setRequiresBatteryNotLow(!manual)
        .build()

    /** Applies the pairs: a periodic run for each active one (15 minutes, the shortest WorkManager allows). */
    fun apply(ctx: Context) {
        val wm = WorkManager.getInstance(ctx)
        val pairs = Graph.syncDb.pairs()
        for (p in pairs) {
            if (p.paused) {
                wm.cancelUniqueWork("sync-periodic-${p.id}")
                continue
            }
            wm.enqueueUniquePeriodicWork(
                "sync-periodic-${p.id}", ExistingPeriodicWorkPolicy.UPDATE,
                PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES).setConstraints(constraints(p))
                    .setInputData(workDataOf("pair" to p.id)).addTag(TAG).addTag("sync-${p.id}").build(),
            )
        }
    }

    /** A round soon: [manual] ("Sync now") ignores the Wi-Fi, charging and battery saver conditions. */
    fun runNow(ctx: Context, p: SyncPair, manual: Boolean) {
        WorkManager.getInstance(ctx).enqueueUniqueWork(
            "sync-now-${p.id}", if (manual) ExistingWorkPolicy.APPEND_OR_REPLACE else ExistingWorkPolicy.KEEP,
            OneTimeWorkRequestBuilder<SyncWorker>().setConstraints(constraints(p, manual))
                .setInputData(workDataOf("pair" to p.id, "manual" to manual)).addTag(TAG).addTag("sync-${p.id}").build(),
        )
    }

    fun runAll(ctx: Context, manual: Boolean) {
        for (p in Graph.syncDb.pairs()) if (!p.paused) runNow(ctx, p, manual)
    }

    fun remove(ctx: Context, id: String) {
        val wm = WorkManager.getInstance(ctx)
        wm.cancelUniqueWork("sync-periodic-$id")
        wm.cancelUniqueWork("sync-now-$id")
        val p = Graph.syncDb.pair(id)
        Graph.syncDb.removePair(id)
        p?.let { releaseFolder(ctx, it) }
    }

    fun resolveBrake(ctx: Context, id: String, answer: String) {
        val db = Graph.syncDb
        db.updateStatus(id) { it.copy(decision = answer, brake = null, state = "idle") }
        db.pair(id)?.let { runNow(ctx, it, manual = true) }
    }

    /** Signing out: the pairs belonged to that NAS (their files stay where they are). */
    fun forgetAll(ctx: Context) {
        WorkManager.getInstance(ctx).cancelAllWorkByTag(TAG)
        for (p in Graph.syncDb.pairs()) {
            Graph.syncDb.removePair(p.id)
            releaseFolder(ctx, p)
        }
    }

    /** Gives back the permission on a phone folder no pair uses any more. */
    fun releaseFolder(ctx: Context, p: SyncPair) {
        if (p.local.startsWith(APP_PREFIX) || Graph.syncDb.pairs().any { it.local == p.local }) return
        runCatching {
            ctx.contentResolver.releasePersistableUriPermission(Uri.parse(p.local),
                Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
        }
    }

    const val TAG = "sync"
}
