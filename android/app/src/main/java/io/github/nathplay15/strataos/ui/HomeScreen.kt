package io.github.nathplay15.strataos.ui

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.OpenInNew
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Events
import io.github.nathplay15.strataos.net.NasEvent
import io.github.nathplay15.strataos.net.Overview
import io.github.nathplay15.strataos.net.StatsHistory
import io.github.nathplay15.strataos.net.SystemInfo
import io.github.nathplay15.strataos.net.Volume
import io.github.nathplay15.strataos.net.manage.PublishedLink
import io.github.nathplay15.strataos.net.manage.publishedApps
import io.github.nathplay15.strataos.net.manage.remoteState

private data class HomeData(val info: SystemInfo, val overview: Overview, val stats: StatsHistory?, val events: Events?, val apps: List<PublishedLink>)

/**
 * The Home tab: a dashboard of the NAS — health, storage, live activity and
 * the latest notifications — with shortcuts to the main places of the app.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(accountAction: @Composable () -> Unit, onOpenFiles: () -> Unit, onOpenPhotos: () -> Unit) {
    val admin = Graph.prefs.server?.isAdmin == true
    val loader = rememberLoad(Unit) {
        val a = api()
        HomeData(
            a.systemInfo(), a.overview(),
            runCatching { a.stats() }.getOrNull(),
            if (admin) runCatching { a.events(0) }.getOrNull() else null,
            if (admin) runCatching { publishedApps(a.remoteState()) }.getOrDefault(emptyList()) else emptyList(),
        )
    }
    Column(Modifier.fillMaxSize()) {
        TopAppBar(title = { Text(stringResource(R.string.tab_home)) }, actions = {
            IconButton(onClick = loader.reload) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) }
            accountAction()
        })
        Refreshable(loader) { d ->
            LazyColumn(Modifier.fillMaxSize(), contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp, 8.dp, 16.dp, 24.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                item { HeroCard(d.info, d.overview) }
                item { QuickActions(onOpenFiles, onOpenPhotos) }
                if (d.apps.isNotEmpty()) item { AppsCard(d.apps) }
                if (d.overview.volumes.isNotEmpty()) item { StorageCard(d.overview) }
                if (d.stats != null && d.stats.history.size >= 2) item { ActivityCard(d.stats) }
                d.events?.let { ev -> if (ev.events.isNotEmpty()) item { NotificationsCard(ev.events) } }
            }
        }
    }
}

/** The published apps of the NAS, opened in the browser. */
@Composable
private fun AppsCard(apps: List<PublishedLink>) {
    val ctx = LocalContext.current
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(stringResource(R.string.a_my_apps), style = MaterialTheme.typography.titleMedium)
            apps.forEach { a ->
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier.fillMaxWidth().clickable { runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(a.url))) } },
                ) {
                    Icon(Icons.Default.OpenInNew, null, Modifier.size(16.dp), tint = MaterialTheme.colorScheme.primary)
                    Column(Modifier.weight(1f).padding(start = 10.dp)) {
                        Text(a.name, style = MaterialTheme.typography.bodyMedium, maxLines = 1)
                        Text(a.url, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
                    }
                }
            }
        }
    }
}

@Composable
private fun HeroCard(info: SystemInfo, ov: Overview) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(info.hostname, style = MaterialTheme.typography.titleLarge)
            Text(stringResource(R.string.status_version, info.version, formatUptime(info.uptime)), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            NasNewerBanner(info.version)
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (ov.alerts.isEmpty()) {
                    Icon(Icons.Default.CheckCircle, null, Modifier.size(18.dp), tint = Tone.OK.color())
                    Text(stringResource(R.string.status_all_good), Modifier.padding(start = 8.dp), style = MaterialTheme.typography.bodyLarge)
                } else {
                    Icon(Icons.Default.Warning, null, Modifier.size(18.dp), tint = Tone.WARN.color())
                    Text(stringResource(R.string.a_alerts_n, ov.alerts.size), Modifier.padding(start = 8.dp), style = MaterialTheme.typography.bodyLarge)
                }
            }
        }
    }
}

@Composable
private fun QuickAction(icon: androidx.compose.ui.graphics.vector.ImageVector, label: String, onClick: () -> Unit) {
    Column(horizontalAlignment = Alignment.CenterHorizontally, modifier = Modifier.clickable(onClick = onClick).padding(4.dp)) {
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.primaryContainer, modifier = Modifier.size(52.dp)) {
            Box(contentAlignment = Alignment.Center) { Icon(icon, null, Modifier.size(24.dp), tint = MaterialTheme.colorScheme.onPrimaryContainer) }
        }
        Text(label, Modifier.padding(top = 6.dp), style = MaterialTheme.typography.labelMedium, textAlign = TextAlign.Center, maxLines = 1)
    }
}

@Composable
private fun QuickActions(onOpenFiles: () -> Unit, onOpenPhotos: () -> Unit) {
    val open = LocalOpenOverlay.current
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceEvenly) {
        QuickAction(Icons.Default.Folder, stringResource(R.string.tab_files), onOpenFiles)
        QuickAction(Icons.Default.PhotoLibrary, stringResource(R.string.a_photos), onOpenPhotos)
        QuickAction(Icons.Default.Backup, stringResource(R.string.tab_backup), { open("backup") })
        QuickAction(Icons.Default.Sync, stringResource(R.string.sync_title), { open("sync") })
    }
}

/** A ring showing [frac] (0..1), drawn as an arc; [color] for the value, track in surface variant. */
@Composable
private fun Ring(frac: Float, color: Color, modifier: Modifier, stroke: androidx.compose.ui.unit.Dp, label: (@Composable () -> Unit)? = null) {
    Box(modifier, contentAlignment = Alignment.Center) {
        val track = MaterialTheme.colorScheme.surfaceVariant
        Canvas(Modifier.fillMaxSize()) {
            val s = stroke.toPx()
            val inset = s / 2
            val arc = Size(size.width - s, size.height - s)
            drawArc(track, 0f, 360f, false, Offset(inset, inset), arc, style = Stroke(s, cap = StrokeCap.Round))
            drawArc(color, -90f, 360f * frac.coerceIn(0f, 1f), false, Offset(inset, inset), arc, style = Stroke(s, cap = StrokeCap.Round))
        }
        label?.invoke()
    }
}

private fun levelTone(level: String): Tone = when (level) {
    "error", "danger", "critical" -> Tone.DANGER
    "warning", "warn" -> Tone.WARN
    else -> Tone.ACCENT
}

@Composable
private fun StorageCard(ov: Overview) {
    val total = ov.volumes.sumOf { it.total }
    val used = ov.volumes.sumOf { it.used }
    val frac = if (total > 0) used.toFloat() / total else 0f
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text(stringResource(R.string.status_storage), style = MaterialTheme.typography.titleMedium)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Ring(frac, if (frac > 0.9f) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary, Modifier.size(84.dp), 9.dp) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        Text("${(frac * 100).toInt()}%", style = MaterialTheme.typography.titleMedium)
                    }
                }
                Spacer(Modifier.width(20.dp))
                Column {
                    Text(stringResource(R.string.status_usage, formatBytes(used), formatBytes(total), formatBytes(total - used)), style = MaterialTheme.typography.bodyMedium)
                    Text(stringResource(R.string.a_disks_n, ov.disks), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            ov.volumes.forEach { v -> VolumeRow(v) }
        }
    }
}

@Composable
private fun VolumeRow(v: Volume) {
    val frac = if (v.total > 0) v.used.toFloat() / v.total else 0f
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
        Ring(frac, when {
            frac > 0.9f -> MaterialTheme.colorScheme.error
            v.health.isNotEmpty() && toneOf(v.health) != Tone.OK -> Tone.WARN.color()
            else -> MaterialTheme.colorScheme.primary
        }, Modifier.size(34.dp), 4.dp)
        Column(Modifier.weight(1f).padding(start = 12.dp)) {
            Text(v.name, style = MaterialTheme.typography.bodyLarge)
            Text(formatBytes(v.used) + " / " + formatBytes(v.total), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        if (v.health.isNotEmpty()) Chip(v.health, toneOf(v.health))
    }
}

/** A small line chart of [values] (0..1), filled under the line. */
@Composable
private fun Spark(values: List<Float>, color: Color, modifier: Modifier) {
    val line = color
    Canvas(modifier) {
        if (values.size < 2) return@Canvas
        val step = size.width / (values.size - 1)
        fun y(v: Float) = size.height - v.coerceIn(0f, 1f) * size.height * 0.92f - size.height * 0.04f
        val path = Path()
        values.forEachIndexed { i, v -> if (i == 0) path.moveTo(0f, y(v)) else path.lineTo(i * step, y(v)) }
        val fill = Path().apply { addPath(path); lineTo(size.width, size.height); lineTo(0f, size.height); close() }
        drawPath(fill, line.copy(alpha = 0.15f))
        drawPath(path, line, style = Stroke(2.dp.toPx(), cap = StrokeCap.Round))
    }
}

@Composable
private fun ActivityCard(stats: StatsHistory) {
    val h = stats.history
    val cpu = h.map { it.cpu.toFloat() / 100f }
    val mem = h.map { s -> if (s.memTotal > 0) s.memUsed.toFloat() / s.memTotal else 0f }
    val last = h.last()
    val accent = MaterialTheme.colorScheme.primary
    val teal = Color(0xFF0E9F8E)
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(stringResource(R.string.home_activity), style = MaterialTheme.typography.titleMedium)
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                Column(Modifier.weight(1f)) {
                    Text(stringResource(R.string.home_cpu) + " · " + last.cpu.toInt() + "%", style = MaterialTheme.typography.labelMedium)
                    Spark(cpu, accent, Modifier.fillMaxWidth().height(56.dp).padding(top = 4.dp))
                }
                Column(Modifier.weight(1f)) {
                    Text(stringResource(R.string.home_ram) + " · " + formatBytes(last.memUsed), style = MaterialTheme.typography.labelMedium)
                    Spark(mem, teal, Modifier.fillMaxWidth().height(56.dp).padding(top = 4.dp))
                }
            }
            Text(
                stringResource(R.string.home_throughput, formatBytes(last.netRx.toLong()) + "/s", formatBytes(last.netTx.toLong()) + "/s", formatBytes(last.diskRead.toLong()) + "/s", formatBytes(last.diskWrite.toLong()) + "/s"),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun NotificationsCard(events: List<NasEvent>) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(stringResource(R.string.status_notifications), style = MaterialTheme.typography.titleMedium)
            events.asReversed().take(5).forEach { e ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    val tone = levelTone(e.level)
                    Icon(if (tone == Tone.DANGER) Icons.Default.Error else if (tone == Tone.WARN) Icons.Default.Warning else Icons.Default.CheckCircle, null, Modifier.size(16.dp), tint = tone.color())
                    Column(Modifier.padding(start = 8.dp)) {
                        Text(e.subject, style = MaterialTheme.typography.bodyMedium, maxLines = 1)
                        Text(formatDate(e.time), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
        }
    }
}

private fun formatUptime(s: Double): String {
    val m = (s / 60).toLong()
    val d = m / 1440
    val h = (m % 1440) / 60
    return if (d > 0) "${d}d ${h}h" else "${h}h ${m % 60}min"
}
