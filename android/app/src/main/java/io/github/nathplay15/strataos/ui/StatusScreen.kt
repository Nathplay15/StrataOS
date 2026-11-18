package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Events
import io.github.nathplay15.strataos.net.Overview
import io.github.nathplay15.strataos.net.SystemInfo
import io.github.nathplay15.strataos.net.Volume

private data class Status(val info: SystemInfo, val overview: Overview, val events: Events?)

@Composable
fun LevelIcon(level: String, modifier: Modifier = Modifier) = when (level) {
    "error", "danger", "critical" -> Icon(Icons.Default.Error, null, modifier, tint = MaterialTheme.colorScheme.error)
    "warning", "warn" -> Icon(Icons.Default.Warning, null, modifier, tint = Tone.WARN.color())
    else -> Icon(Icons.Default.Info, null, modifier, tint = MaterialTheme.colorScheme.primary)
}

/** Health, storage use, alerts and (administrators) the latest NAS notifications. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun StatusScreen(onBack: (() -> Unit)? = null) {
    val admin = Graph.prefs.server?.isAdmin == true
    val loader = rememberLoad(Unit) {
        val api = api()
        Status(api.systemInfo(), api.overview(), if (admin) api.events(0) else null)
    }
    Column(Modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text(stringResource(R.string.tab_status)) },
            navigationIcon = { if (onBack != null) IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
            actions = { IconButton(onClick = loader.reload) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) } },
        )
        Refreshable(loader) { st ->
            Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp, 4.dp, 16.dp, 24.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                val alerts = st.overview.alerts
                // The NAS: a blue card with its picture, version and uptime.
                Box(Modifier.fillMaxWidth().background(strata.header, MaterialTheme.shapes.large).padding(20.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                            Text(st.info.hostname, style = MaterialTheme.typography.headlineSmall, color = Color.White)
                            Text(stringResource(R.string.status_version, st.info.version, formatUptime(st.info.uptime)), style = MaterialTheme.typography.bodyMedium, color = Color.White.copy(alpha = 0.85f))
                            if (st.info.load.isNotEmpty()) Text(stringResource(R.string.status_load, "%.2f".format(st.info.load[0])), style = MaterialTheme.typography.bodySmall, color = Color.White.copy(alpha = 0.75f))
                        }
                        NasIllustration(Modifier.size(92.dp, 76.dp), healthy = alerts.isEmpty())
                    }
                }
                NasNewerBanner(st.info.version)
                SectionCard(stringResource(R.string.status_alerts), trailing = {
                    Chip(if (alerts.isEmpty()) stringResource(R.string.ui_healthy) else alerts.size.toString(), if (alerts.isEmpty()) Tone.OK else Tone.WARN)
                }) {
                    if (alerts.isEmpty()) Row(verticalAlignment = Alignment.CenterVertically) {
                        Icon(Icons.Default.CheckCircle, null, tint = Tone.OK.color())
                        Text(stringResource(R.string.status_all_good), Modifier.padding(start = 10.dp))
                    }
                    alerts.forEach { a -> Row(verticalAlignment = Alignment.CenterVertically) { LevelIcon(a.level); Text(a.message, Modifier.padding(start = 10.dp)) } }
                }
                if (st.overview.volumes.isNotEmpty()) SectionCard(stringResource(R.string.status_storage)) {
                    st.overview.volumes.forEachIndexed { i, v ->
                        if (i > 0) HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
                        VolumeLine(v)
                    }
                }
                st.events?.let { ev ->
                    SectionCard(stringResource(R.string.status_notifications)) {
                        if (ev.events.isEmpty()) Text(stringResource(R.string.status_no_notifications), style = MaterialTheme.typography.bodySmall)
                        ev.events.asReversed().take(30).forEach { e ->
                            Row {
                                LevelIcon(e.level, Modifier.padding(top = 2.dp).size(20.dp))
                                Column(Modifier.padding(start = 12.dp)) {
                                    Text(e.subject, style = MaterialTheme.typography.bodyLarge)
                                    if (e.message.isNotBlank()) Text(e.message, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                    Text(formatDate(e.time), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                            }
                        }
                    }
                }
                if (!admin) Text(stringResource(R.string.status_admin_only), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

/** A volume of the overview: icon, name, health pill, thin usage bar and the figures. */
@Composable
fun VolumeLine(v: Volume) {
    val frac = if (v.total > 0) v.used.toFloat() / v.total else 0f
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(42.dp).background(MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.6f), RoundedCornerShape(12.dp)), contentAlignment = Alignment.Center) {
            Icon(Icons.Default.Storage, null, Modifier.size(22.dp), tint = MaterialTheme.colorScheme.primary)
        }
        Column(Modifier.weight(1f).padding(start = 14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(v.name, Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
                if (v.health.isNotEmpty()) Chip(v.health, toneOf(v.health))
            }
            ThinBar(frac, Modifier.fillMaxWidth().padding(vertical = 6.dp))
            Text(stringResource(R.string.status_usage, formatBytes(v.used), formatBytes(v.total), formatBytes(v.free)), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

fun formatUptime(s: Double): String {
    val m = (s / 60).toLong()
    val d = m / 1440
    val h = (m % 1440) / 60
    return if (d > 0) "${d}d ${h}h" else "${h}h ${m % 60}min"
}
