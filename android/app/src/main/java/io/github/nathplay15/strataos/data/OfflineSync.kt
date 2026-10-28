package io.github.nathplay15.strataos.data

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.fileInfo
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** The favorites of this phone (no dependency injection: one instance). */
val favorites: Favorites by lazy { Favorites(Graph.app) }

/**
 * Keeps the offline favorites up to date: every few hours on an unmetered
 * network, and right away when a file is made available offline.
 */
class OfflineSyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        val api = Graph.api() ?: return@withContext Result.success()
        var retry = false
        for (f in favorites.all().filter { it.offline && !it.dir }) {
            try {
                val info = try {
                    api.fileInfo(f.path)
                } catch (e: ApiException) {
                    if (e.status == 404 || e.status == 403) null else throw e
                }
                val copy = favorites.copyOf(f.path)
                when (syncAction(f, info?.size, info?.modTime, copy.exists())) {
                    SyncAction.MISSING -> favorites.update(f.path) { it.copy(missing = true, synced = System.currentTimeMillis()) }
                    SyncAction.KEEP -> favorites.update(f.path) { it.copy(missing = false, synced = System.currentTimeMillis()) }
                    SyncAction.DOWNLOAD -> {
                        copy.parentFile?.mkdirs()
                        api.download(f.path, copy)
                        favorites.update(f.path) { it.copy(size = info!!.size, modTime = info.modTime, missing = false, synced = System.currentTimeMillis()) }
                    }
                }
            } catch (e: IOException) {
                retry = true // NAS unreachable: try again later
            }
        }
        if (retry) Result.retry() else Result.success()
    }

    companion object {
        private const val PERIODIC = "offline-sync"
        private const val NOW = "offline-sync-now"

        fun schedule(ctx: Context) {
            val c = Constraints.Builder().setRequiredNetworkType(NetworkType.UNMETERED).setRequiresBatteryNotLow(true).build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(PERIODIC, ExistingPeriodicWorkPolicy.KEEP,
                PeriodicWorkRequestBuilder<OfflineSyncWorker>(6, TimeUnit.HOURS).setConstraints(c).build())
        }

        fun syncNow(ctx: Context) {
            WorkManager.getInstance(ctx).enqueueUniqueWork(NOW, ExistingWorkPolicy.REPLACE,
                OneTimeWorkRequestBuilder<OfflineSyncWorker>().setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build()).build())
        }

        fun cancel(ctx: Context) {
            WorkManager.getInstance(ctx).cancelUniqueWork(PERIODIC)
            WorkManager.getInstance(ctx).cancelUniqueWork(NOW)
        }
    }
}
