package io.github.nathplay15.strataos.backup

import android.content.Context
import android.content.pm.ServiceInfo
import android.os.Build
import android.provider.MediaStore
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
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.CreateUpload
import io.github.nathplay15.strataos.net.ExistsFile
import io.github.nathplay15.strataos.net.ExistsRequest
import io.github.nathplay15.strataos.notify.Notifier
import io.github.nathplay15.strataos.upload.ApiTransport
import io.github.nathplay15.strataos.upload.ChunkedUploader
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/**
 * The automatic photo and video backup: finds what is new in the chosen
 * albums, asks the NAS which files it already has, and sends the rest with
 * resumable uploads. Runs every hour and soon after new photos appear, under
 * the Wi-Fi / charging conditions chosen. Never deletes anything on the phone.
 */
class BackupWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        val settings = BackupSettings(applicationContext)
        try {
            if (!settings.enabled || settings.destination.isEmpty()) return@withContext Result.success()
            lock.withLock { run(settings) }
        } finally {
            if (settings.enabled) BackupScheduler.watchNewMedia(applicationContext)
        }
    }

    private suspend fun run(settings: BackupSettings): Result {
        val ctx = applicationContext
        val api = Graph.api() ?: return Result.failure()
        if (!MediaScanner.hasPermission(ctx)) {
            settings.lastResult = ctx.getString(R.string.backup_no_permission)
            return Result.failure()
        }
        val db = Graph.backupDb
        val pending = BackupPlanner.pending(MediaScanner.scan(ctx, settings.buckets, settings.videos), db.doneKeys(), settings.options())
        if (pending.isEmpty()) {
            settings.lastRun = System.currentTimeMillis()
            settings.lastResult = ctx.getString(R.string.backup_up_to_date)
            return Result.success()
        }
        val title = ctx.getString(R.string.backup_running)
        var done = 0
        var sent = 0
        var skipped = 0
        foreground(title, ctx.getString(R.string.backup_progress, 0, pending.size), 0, pending.size)
        val uploader = ChunkedUploader(ApiTransport(api), db)
        try {
            for ((folder, batch) in BackupPlanner.batches(pending, settings.destination)) {
                // Already on the NAS (reinstalled app, another phone, copied by hand): skipped.
                val found = api.exists(ExistsRequest(folder, batch.map { ExistsFile(it.name, it.size) })).files
                for ((i, item) in batch.withIndex()) {
                    if (isStopped) return Result.retry()
                    if (found.getOrNull(i)?.exists == true) {
                        db.markDone(item.key, folder + "/" + found[i].`as`.ifEmpty { item.name })
                        skipped++
                    } else {
                        val st = uploader.upload(item.key, CreateUpload(folder, item.name, item.size, item.originalDate, mkdirs = true), MediaScanner.source(ctx, item)) { s, t ->
                            if (t > 0) setProgressAsync(androidx.work.workDataOf("done" to done, "total" to pending.size, "file" to item.name, "bytes" to s, "size" to t))
                        }
                        db.markDone(item.key, st.path)
                        sent++
                    }
                    done++
                    foreground(title, ctx.getString(R.string.backup_progress, done, pending.size), done, pending.size)
                }
            }
        } catch (e: ApiException) {
            settings.lastResult = ctx.getString(R.string.backup_failed, e.message ?: "")
            if (e.status == 401) {
                Notifier.signedOut(ctx)
                return Result.failure()
            }
            return if (e.status >= 500 || e.status == 429) Result.retry() else Result.failure()
        } catch (e: IOException) {
            settings.lastResult = ctx.getString(R.string.backup_failed, e.message ?: e.javaClass.simpleName)
            return Result.retry()
        } catch (e: SecurityException) {
            settings.lastResult = ctx.getString(R.string.backup_no_permission)
            return Result.failure()
        }
        settings.lastRun = System.currentTimeMillis()
        settings.lastResult = ctx.getString(R.string.backup_result, sent, skipped)
        return Result.success()
    }

    private suspend fun foreground(title: String, text: String, done: Int, total: Int) {
        val n = Notifier.progress(applicationContext, Notifier.BACKUP, title, text, done, total)
        runCatching {
            setForeground(
                if (Build.VERSION.SDK_INT >= 29) ForegroundInfo(Notifier.BACKUP_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
                else ForegroundInfo(Notifier.BACKUP_ID, n),
            )
        }
    }

    companion object {
        private val lock = Mutex()
    }
}

object BackupScheduler {
    private const val PERIODIC = "backup-periodic"
    private const val TRIGGER = "backup-new-media"
    private const val NOW = "backup-now"

    private fun constraints(s: BackupSettings, media: Boolean = false) = Constraints.Builder()
        .setRequiredNetworkType(if (s.wifiOnly) NetworkType.UNMETERED else NetworkType.CONNECTED)
        .setRequiresCharging(s.chargingOnly)
        .apply {
            if (media) {
                addContentUriTrigger(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, true)
                addContentUriTrigger(MediaStore.Video.Media.EXTERNAL_CONTENT_URI, true)
                setTriggerContentUpdateDelay(30, TimeUnit.SECONDS)
                setTriggerContentMaxDelay(5, TimeUnit.MINUTES)
            }
        }
        .build()

    /** Applies the settings: hourly runs and a run when new photos or videos appear. */
    fun apply(ctx: Context) {
        val s = BackupSettings(ctx)
        val wm = WorkManager.getInstance(ctx)
        if (!s.enabled) {
            wm.cancelUniqueWork(PERIODIC)
            wm.cancelUniqueWork(TRIGGER)
            return
        }
        wm.enqueueUniquePeriodicWork(
            PERIODIC, ExistingPeriodicWorkPolicy.UPDATE,
            PeriodicWorkRequestBuilder<BackupWorker>(1, TimeUnit.HOURS).setConstraints(constraints(s)).addTag("backup").build(),
        )
        watchNewMedia(ctx, replace = true)
    }

    /** A content-URI trigger fires once: it is enqueued again after each run. */
    fun watchNewMedia(ctx: Context, replace: Boolean = false) {
        val s = BackupSettings(ctx)
        WorkManager.getInstance(ctx).enqueueUniqueWork(
            TRIGGER, if (replace) ExistingWorkPolicy.REPLACE else ExistingWorkPolicy.KEEP,
            OneTimeWorkRequestBuilder<BackupWorker>().setConstraints(constraints(s, media = true)).addTag("backup").build(),
        )
    }

    fun runNow(ctx: Context) {
        val s = BackupSettings(ctx)
        WorkManager.getInstance(ctx).enqueueUniqueWork(
            NOW, ExistingWorkPolicy.KEEP,
            OneTimeWorkRequestBuilder<BackupWorker>().setConstraints(constraints(s)).addTag("backup").build(),
        )
    }

    fun cancelAll(ctx: Context) {
        WorkManager.getInstance(ctx).cancelAllWorkByTag("backup")
    }
}
