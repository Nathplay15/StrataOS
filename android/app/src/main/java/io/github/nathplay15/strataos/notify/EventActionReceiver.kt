package io.github.nathplay15.strataos.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.app.PendingIntent
import androidx.core.app.NotificationManagerCompat
import io.github.nathplay15.strataos.Graph
import kotlin.concurrent.thread

/**
 * The answer of a notification action button: marks the NAS notifications up
 * to the event as read (the bell of the web interface) and clears it, without
 * opening the app.
 */
class EventActionReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != ACTION_MARK_READ) return
        val id = intent.getLongExtra("id", -1)
        if (id < 0) return
        val pending = goAsync()
        thread {
            try {
                runCatching { Graph.api()?.markRead(id) }
                NotificationManagerCompat.from(ctx).cancel("nas", id.toInt())
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        const val ACTION_MARK_READ = "io.github.nathplay15.strataos.action.MARK_READ"

        fun markRead(ctx: Context, id: Long): PendingIntent = PendingIntent.getBroadcast(
            ctx, id.toInt(),
            Intent(ctx, EventActionReceiver::class.java).setAction(ACTION_MARK_READ).putExtra("id", id),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }
}
