package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
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

private data class Status(val info: SystemInfo, val overview: Overview, val events: Events?)

@Composable
private fun levelIcon(level: String) = when (level) {
    "error", "danger", "critical" -> Icon(Icons.Default.Error, null, tint = MaterialTheme.colorScheme.error)
    "warning", "warn" -> Icon(Icons.Default.Warning, null, tint = Color(0xFFE0A100))
    else -> Icon(Icons.Default.Info, null, tint = MaterialTheme.colorScheme.primary)
}

/** Health, storage use, alerts and (administrators) the latest NAS notifications. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun StatusScreen() {
    val admin = Graph.prefs.server?.isAdmin == true
    val loader = rememberLoader(Unit) {
        val api = Graph.api()!!
        Status(api.systemInfo(), api.overview(), if (admin) api.events(0) else null)
    }
    Column(Modifier.fillMaxSize()) {
        TopAppBar(title = { Text(stringResource(R.string.tab_status)) }, actions = { IconButton(onClick = loader.reload) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) } })
        LoadedContent(loader) { st ->
            Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Card(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp)) {
                        Text(st.info.hostname, style = MaterialTheme.typography.titleLarge)
                        Text(stringResource(R.string.status_version, st.info.version, formatUptime(st.info.uptime)), style = MaterialTheme.typography.bodyMedium)
                        if (st.info.load.isNotEmpty()) Text(stringResource(R.string.status_load, "%.2f".format(st.info.load[0])), style = MaterialTheme.typography.bodySmall)
                    }
                }
                val alerts = st.overview.alerts
                Card(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(stringResource(R.string.status_alerts), style = MaterialTheme.typography.titleMedium)
                        if (alerts.isEmpty()) Row(verticalAlignment = Alignment.CenterVertically) {
                            Icon(Icons.Default.CheckCircle, null, tint = Color(0xFF1E9E5A))
                            Text(stringResource(R.string.status_all_good), Modifier.padding(start = 8.dp))
                        }
                        alerts.forEach { a -> Row(verticalAlignment = Alignment.CenterVertically) { levelIcon(a.level); Text(a.message, Modifier.padding(start = 8.dp)) } }
                    }
                }
                if (st.overview.volumes.isNotEmpty()) Card(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                        Text(stringResource(R.string.status_storage), style = MaterialTheme.typography.titleMedium)
                        st.overview.volumes.forEach { v ->
                            Column {
                                Row {
                                    Text(v.name, Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
                                    Text(v.health, style = MaterialTheme.typography.bodySmall, color = if (v.health.lowercase() in setOf("ok", "healthy", "online", "clean", "active")) Color(0xFF1E9E5A) else MaterialTheme.colorScheme.error)
                                }
                                val frac = if (v.total > 0) v.used.toFloat() / v.total else 0f
                                LinearProgressIndicator(progress = { frac }, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
                                    color = if (frac > 0.9f) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary)
                                Text(stringResource(R.string.status_usage, formatBytes(v.used), formatBytes(v.total), formatBytes(v.free)), style = MaterialTheme.typography.bodySmall)
                            }
                        }
                    }
                }
                st.events?.let { ev ->
                    Card(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text(stringResource(R.string.status_notifications), style = MaterialTheme.typography.titleMedium)
                            if (ev.events.isEmpty()) Text(stringResource(R.string.status_no_notifications), style = MaterialTheme.typography.bodySmall)
                            ev.events.asReversed().take(30).forEach { e ->
                                Row {
                                    levelIcon(e.level)
                                    Column(Modifier.padding(start = 8.dp)) {
                                        Text(e.subject, style = MaterialTheme.typography.bodyLarge)
                                        Text(e.message, style = MaterialTheme.typography.bodySmall)
                                        Text(formatDate(e.time), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                    }
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

private fun formatUptime(s: Double): String {
    val m = (s / 60).toLong()
    val d = m / 1440
    val h = (m % 1440) / 60
    return if (d > 0) "${d}d ${h}h" else "${h}h ${m % 60}min"
}

