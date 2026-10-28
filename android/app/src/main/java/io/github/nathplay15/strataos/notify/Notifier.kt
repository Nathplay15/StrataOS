package io.github.nathplay15.strataos.notify

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.MainActivity

object Notifier {
    const val ALERTS = "alerts"
    const val INFO = "info"
    const val BACKUP = "backup"
    const val UPLOADS = "uploads"
    const val SYNC = "sync"
    const val BACKUP_ID = 1
    const val SIGNED_OUT_ID = 2

    fun createChannels(ctx: Context) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannels(listOf(
            NotificationChannel(ALERTS, ctx.getString(R.string.channel_alerts), NotificationManager.IMPORTANCE_HIGH),
            NotificationChannel(INFO, ctx.getString(R.string.channel_info), NotificationManager.IMPORTANCE_DEFAULT),
            NotificationChannel(BACKUP, ctx.getString(R.string.channel_backup), NotificationManager.IMPORTANCE_LOW),
            NotificationChannel(UPLOADS, ctx.getString(R.string.channel_uploads), NotificationManager.IMPORTANCE_LOW),
            NotificationChannel(SYNC, ctx.getString(R.string.sync_title), NotificationManager.IMPORTANCE_LOW),
        ))
    }

    fun openApp(ctx: Context, tab: String? = null): PendingIntent = PendingIntent.getActivity(
        ctx, tab.hashCode(), Intent(ctx, MainActivity::class.java).putExtra("tab", tab).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    fun canPost(ctx: Context) = ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED ||
        android.os.Build.VERSION.SDK_INT < 33

    /** A notification of the NAS (failing disk, failed backup…). */
    fun nasEvent(ctx: Context, id: Long, level: String, subject: String, message: String, host: String) {
        if (!canPost(ctx)) return
        val n = NotificationCompat.Builder(ctx, if (level == "info") INFO else ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(if (host.isNotEmpty()) "$subject · $host" else subject)
            .setContentText(message)
            .setStyle(NotificationCompat.BigTextStyle().bigText(message))
            .setPriority(if (level == "error") NotificationCompat.PRIORITY_HIGH else NotificationCompat.PRIORITY_DEFAULT)
            .setContentIntent(openApp(ctx, "status"))
            .setAutoCancel(true)
            .build()
        @Suppress("MissingPermission")
        NotificationManagerCompat.from(ctx).notify("nas", id.toInt(), n)
    }

    fun signedOut(ctx: Context) {
        if (!canPost(ctx)) return
        val n = NotificationCompat.Builder(ctx, ALERTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(ctx.getString(R.string.signed_out_title))
            .setContentText(ctx.getString(R.string.signed_out_text))
            .setContentIntent(openApp(ctx))
            .setAutoCancel(true)
            .build()
        @Suppress("MissingPermission")
        NotificationManagerCompat.from(ctx).notify(SIGNED_OUT_ID, n)
    }

    fun progress(ctx: Context, channel: String, title: String, text: String, done: Int, total: Int) =
        NotificationCompat.Builder(ctx, channel)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setOnlyAlertOnce(true)
            .setOngoing(true)
            .setProgress(total, done, total == 0)
            .setContentIntent(openApp(ctx, "backup"))
            .build()
}
