package io.github.nathplay15.strataos.notify

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.net.ApiException
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** Shows the NAS events not shown yet (push and polling share the cursor, so nothing shows twice). */
object EventSync {
    @Synchronized
    fun show(ctx: Context, id: Long, level: String, subject: String, message: String, host: String) {
        val p = Graph.prefs
        if (id <= p.lastEventId) return
        p.lastEventId = id
        Notifier.nasEvent(ctx, id, level, subject, message, host)
    }

    /** Reads the feed; the first time it only sets the cursor (no flood of old alerts). */
    fun poll(ctx: Context) {
        val api = Graph.api() ?: return
        val p = Graph.prefs
        val since = p.lastEventId
        val ev = api.events(maxOf(0, since))
        if (since < 0) {
            p.lastEventId = ev.cursor
            return
        }
        val host = p.server?.hostname ?: ""
        ev.events.forEach { show(ctx, it.id, it.level, it.subject, it.message, host) }
        if (ev.cursor < since) p.lastEventId = ev.cursor // feed reset on the NAS
    }
}

/** Fallback without a UnifiedPush distributor: checks the NAS every 15 minutes. */
class EventsWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        try {
            EventSync.poll(applicationContext)
            Result.success()
        } catch (e: ApiException) {
            if (e.status == 401) Result.failure() else Result.retry()
        } catch (e: Exception) {
            Result.retry()
        }
    }

    companion object {
        fun schedule(ctx: Context) {
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(
                "nas-events", ExistingPeriodicWorkPolicy.KEEP,
                PeriodicWorkRequestBuilder<EventsWorker>(15, TimeUnit.MINUTES)
                    .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build()).build(),
            )
        }

        fun cancel(ctx: Context) = WorkManager.getInstance(ctx).cancelUniqueWork("nas-events")
    }
}
