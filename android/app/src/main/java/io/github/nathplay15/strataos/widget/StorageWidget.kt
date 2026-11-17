package io.github.nathplay15.strataos.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.MainActivity
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatTime
import kotlin.concurrent.thread

/**
 * The storage widget of the home screen: NAS name, health (alerts), total
 * usage and the time of the last refresh. Refreshed by the system every
 * 30 minutes, when the app starts and after pairing or signing out.
 */
class StorageWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        val pending = goAsync()
        thread {
            try {
                ids.forEach { id -> manager.updateAppWidget(id, build(context)) }
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        /** Asks every placed widget to refresh (after pairing, sign out, NAS events). */
        fun refresh(context: Context) {
            context.sendBroadcast(Intent(context, StorageWidget::class.java).setAction(AppWidgetManager.ACTION_APPWIDGET_UPDATE)
                .putExtra(AppWidgetManager.EXTRA_APPWIDGET_IDS, AppWidgetManager.getInstance(context).getAppWidgetIds(ComponentName(context, StorageWidget::class.java))))
        }

        /** The views of the widget: data of the NAS when reachable, a readable state otherwise. */
        fun build(context: Context): RemoteViews {
            val open = PendingIntent.getActivity(
                context, 0, Intent(context, MainActivity::class.java),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
            val v = RemoteViews(context.packageName, R.layout.storage_widget)
            v.setOnClickPendingIntent(R.id.widgetRoot, open)
            val api = Graph.api()
            if (api == null) {
                v.setTextViewText(R.id.widgetHost, context.getString(R.string.app_name))
                v.setTextViewText(R.id.widgetHealth, context.getString(R.string.widget_not_paired))
                v.setTextColor(R.id.widgetHealth, 0xFF8AB4F8.toInt())
                v.setProgressBar(R.id.widgetProgress, 100, 0, false)
                v.setTextViewText(R.id.widgetUsage, "")
                v.setTextViewText(R.id.widgetLoad, "")
                v.setTextViewText(R.id.widgetUpdated, "")
                return v
            }
            try {
                val info = api.systemInfo()
                val ov = api.overview()
                val total = ov.volumes.sumOf { it.total }
                val used = ov.volumes.sumOf { it.used }
                val pct = if (total > 0) (used * 100 / total).toInt() else 0
                v.setTextViewText(R.id.widgetHost, info.hostname.ifEmpty { context.getString(R.string.app_name) })
                if (ov.alerts.isEmpty()) {
                    v.setTextViewText(R.id.widgetHealth, context.getString(R.string.status_all_good))
                    v.setTextColor(R.id.widgetHealth, 0xFF81C995.toInt())
                } else {
                    v.setTextViewText(R.id.widgetHealth, ov.alerts.first().message)
                    v.setTextColor(R.id.widgetHealth, if (ov.alerts.any { it.level in setOf("error", "danger", "critical") }) 0xFFF28B82.toInt() else 0xFFFDD663.toInt())
                }
                v.setProgressBar(R.id.widgetProgress, 100, pct, false)
                v.setTextViewText(R.id.widgetUsage, context.getString(R.string.status_usage, formatBytes(used), formatBytes(total), formatBytes(total - used)))
                val sample = api.stats().history.lastOrNull()
                if (sample != null) {
                    val mem = context.getString(R.string.status_usage, formatBytes(sample.memUsed), formatBytes(sample.memTotal), formatBytes(sample.memTotal - sample.memUsed))
                    v.setTextViewText(R.id.widgetLoad, context.getString(R.string.widget_load, "%.0f %%".format(sample.cpu), mem))
                } else {
                    v.setTextViewText(R.id.widgetLoad, "")
                }
                v.setTextViewText(R.id.widgetUpdated, context.getString(R.string.widget_updated, formatTime(System.currentTimeMillis())))
            } catch (e: Exception) {
                v.setTextViewText(R.id.widgetHost, Graph.prefs.server?.hostname ?: context.getString(R.string.app_name))
                v.setTextViewText(R.id.widgetHealth, context.getString(R.string.widget_unreachable))
                v.setTextColor(R.id.widgetHealth, 0xFFFDD663.toInt())
                v.setProgressBar(R.id.widgetProgress, 100, 0, false)
                v.setTextViewText(R.id.widgetUsage, "")
                v.setTextViewText(R.id.widgetLoad, "")
                v.setTextViewText(R.id.widgetUpdated, "")
            }
            return v
        }
    }
}
