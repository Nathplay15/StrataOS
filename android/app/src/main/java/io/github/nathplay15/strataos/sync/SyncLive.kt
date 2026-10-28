package io.github.nathplay15.strataos.sync

import android.content.Context
import io.github.nathplay15.strataos.Graph
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.runInterruptible

/**
 * While the app is in the foreground: a round of every pair when it opens,
 * one long poll per pair on the NAS change feed (a change there starts a
 * round within seconds), and a look at the phone folders every two minutes
 * (documents providers do not report changes made by other apps).
 */
object SyncLive {
    private var job: Job? = null

    @Synchronized
    fun start(ctx: Context) {
        if (job != null) return
        val app = ctx.applicationContext
        job = CoroutineScope(SupervisorJob() + Dispatchers.IO).launch {
            val pairs = runCatching { Graph.syncDb.pairs().filter { !it.paused } }.getOrDefault(emptyList())
            if (pairs.isEmpty() || Graph.api() == null) return@launch
            SyncScheduler.runAll(app, manual = false)
            for (p in pairs) launch { poll(app, p) }
            while (isActive) {
                delay(120_000)
                SyncScheduler.runAll(app, manual = false)
            }
        }
    }

    @Synchronized
    fun stop() {
        job?.cancel()
        job = null
    }

    /** After the pairs changed. */
    fun restart(ctx: Context) {
        stop()
        start(ctx)
    }

    private suspend fun CoroutineScope.poll(ctx: Context, p: SyncPair) {
        while (isActive) {
            val api = Graph.api() ?: return
            val cursor = Graph.syncDb.cursor(p.id)
            if (cursor.isEmpty()) { // the first round has not run yet
                delay(10_000)
                continue
            }
            val c = runInterruptible { runCatching { ApiRemote(api).changes(p.remote, cursor, 50) }.getOrNull() }
            if (c == null) {
                delay(30_000)
                continue
            }
            if (c.reset || c.entries.isNotEmpty() || c.deleted.isNotEmpty()) {
                SyncScheduler.runNow(ctx, p, manual = false)
                delay(5_000) // the round moves the cursor on
            }
        }
    }
}
