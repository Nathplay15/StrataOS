package io.github.nathplay15.strataos.ui.fleet

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Key
import androidx.compose.material.icons.filled.OpenInBrowser
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.FleetNas
import io.github.nathplay15.strataos.net.FleetRun
import io.github.nathplay15.strataos.net.FleetUpdateReq
import io.github.nathplay15.strataos.net.NasJob
import io.github.nathplay15.strataos.net.addFleetNas
import io.github.nathplay15.strataos.net.cancelFleetRun
import io.github.nathplay15.strataos.net.fleet
import io.github.nathplay15.strataos.net.fleetCheck
import io.github.nathplay15.strataos.net.fleetCode
import io.github.nathplay15.strataos.net.fleetManagers
import io.github.nathplay15.strataos.net.fleetRuns
import io.github.nathplay15.strataos.net.removeFleetManager
import io.github.nathplay15.strataos.net.removeFleetNas
import io.github.nathplay15.strataos.net.setFleetManager
import io.github.nathplay15.strataos.net.startFleetUpdate
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate

// Multi-NAS console: the NAS this one watches (health, versions, updates),
// grouped updates with their progress, and the consoles allowed to watch this
// NAS. Routes: "list", "nas:<id>", "access".

/** A NAS address the console accepts (a name or an address, no scheme or path). */
fun validFleetHost(h: String): Boolean = h.isNotBlank() && h.none { it in "/ @?#" }

/** The address of the web interface of a NAS of the console. */
fun fleetWebUrl(host: String): String = "https://" + (if (host.contains(':') && !host.startsWith("[")) "[$host]" else host) + ":7443/"

private class Console(val nas: List<FleetNas>, val runs: List<FleetRun>)

@Composable
fun FleetArea(back: () -> Unit) {
    var route by rememberSaveable { mutableStateOf("list") }
    var poll by remember { mutableStateOf(false) }
    val data = rememberData(poll, every = if (poll) 3000 else 0) { api -> Console(api.fleet(), api.fleetRuns()).also { c -> poll = c.runs.any { it.running } } }
    val (kind, id) = route.split(":", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
    val nas = data.value?.nas?.firstOrNull { it.id == id }
    when {
        kind == "access" -> AccessScreen(onBack = { route = "list" })
        kind == "nas" && nas != null -> NasDetail(nas, onBack = { route = "list"; data.reload() })
        else -> ConsoleList(data, back) { route = it }
    }
}

@Composable
private fun ConsoleList(data: io.github.nathplay15.strataos.ui.adminkit.Data<Console>, back: () -> Unit, go: (String) -> Unit) {
    val actions = rememberActions()
    var selected by remember { mutableStateOf(setOf<String>()) }
    var adding by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }
    var job by remember { mutableStateOf<NasJob?>(null) }
    AreaScreen(stringResource(R.string.area_fleet), back, actions = {
        IconButton(onClick = { adding = true }) { Icon(Icons.Default.Add, stringResource(R.string.fleet_add)) }
        IconButton(onClick = { go("access") }) { Icon(Icons.Default.Key, stringResource(R.string.fleet_access)) }
    }) { pad ->
        Refreshable(data, pad) { c ->
            ScrollColumn {
                if (c.nas.size <= 1) Text(stringResource(R.string.fleet_empty), style = MaterialTheme.typography.bodyMedium)
                c.nas.forEach { n -> NasCard(n, selected = n.id in selected, onSelect = if (n.local) null else { on -> selected = if (on) selected + n.id else selected - n.id }) { go("nas:${n.id}") } }
                if (selected.isNotEmpty()) Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(enabled = !actions.busy, onClick = { actions.run { job = it.fleetCheck(selected.toList()) } }) { Text(stringResource(R.string.fleet_check)) }
                    Button(enabled = !actions.busy && c.runs.none { it.running }, onClick = { updating = true }) { Text(stringResource(R.string.fleet_update_n, selected.size)) }
                }
                if (c.runs.isNotEmpty()) SectionCard(stringResource(R.string.fleet_runs)) {
                    c.runs.take(5).forEach { r -> RunView(r) { actions.run(after = data.reload) { it.cancelFleetRun(r.id) } }; HorizontalDivider() }
                }
            }
        }
    }
    if (adding) AddDialog(onDismiss = { adding = false }) { h, code -> adding = false; actions.run(after = data.reload) { it.addFleetNas(h, code) } }
    if (updating) UpdateDialog(selected.size, onDismiss = { updating = false }) { kind, strategy, reboot, dry ->
        updating = false
        actions.run(after = { selected = emptySet(); data.reload() }) { it.startFleetUpdate(FleetUpdateReq(selected.toList(), kind, strategy, reboot, dry)) }
    }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

@Composable
private fun NasCard(n: FleetNas, selected: Boolean, onSelect: ((Boolean) -> Unit)?, open: () -> Unit) {
    val s = n.summary
    Card(Modifier.fillMaxWidth().clickable(onClick = open)) {
        Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            if (onSelect != null) Checkbox(selected, onSelect)
            Column(Modifier.weight(1f).padding(start = 4.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(n.name.ifEmpty { n.host }, style = MaterialTheme.typography.titleMedium)
                    when {
                        n.local -> StatusBadge(stringResource(R.string.fleet_this_nas), Tone.Accent)
                        !n.online -> StatusBadge(stringResource(R.string.fleet_offline), Tone.Bad)
                        n.errors > 0 -> StatusBadge(stringResource(R.string.fleet_alerts, n.errors), Tone.Bad)
                        (s?.alerts?.size ?: 0) > 0 -> StatusBadge(stringResource(R.string.fleet_alerts, s!!.alerts.size), Tone.Warn)
                        else -> StatusBadge(stringResource(R.string.fleet_healthy), Tone.Ok)
                    }
                    if (n.haWith.isNotEmpty()) StatusBadge("HA", Tone.Neutral)
                }
                if (n.host.isNotEmpty()) Text(n.host, style = MaterialTheme.typography.bodySmall)
                if (s != null) {
                    Text(stringResource(R.string.fleet_version_uptime, s.version, s.uptimeDays.toInt()), style = MaterialTheme.typography.bodySmall)
                    val up = s.updates
                    if (up.candidate.isNotEmpty() || up.system > 0) Text(stringResource(R.string.fleet_updates_available, up.candidate.ifEmpty { "—" }, up.system),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.primary)
                    if (s.backups.failed > 0) Text(stringResource(R.string.fleet_backups_failed, s.backups.failed), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
                }
                if (n.error.isNotEmpty()) Text(n.error, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
            }
        }
    }
}

@Composable
private fun RunView(r: FleetRun, cancel: () -> Unit) {
    Column(Modifier.padding(vertical = 4.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(if (r.kind == "strata") R.string.fleet_kind_strata else R.string.fleet_kind_system) + if (r.dryRun) " · " + stringResource(R.string.fleet_dry_run) else "",
                Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
            StatusBadge(runState(r.status), when (r.status) { "success" -> Tone.Ok; "failed" -> Tone.Bad; "running" -> Tone.Accent; else -> Tone.Neutral })
        }
        Text(formatDate(r.started), style = MaterialTheme.typography.bodySmall)
        if (r.running) LinearProgressIndicator(progress = { (r.totals.progress / 100).toFloat() }, Modifier.fillMaxWidth())
        r.items.forEach { it ->
            Text("${it.name}: ${runState(it.state)}" + listOf(it.version, it.note, it.error).filter { s -> s.isNotEmpty() }.joinToString(" · ", prefix = " ").trimEnd(),
                style = MaterialTheme.typography.bodySmall, color = if (it.error.isNotEmpty()) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant)
        }
        if (r.running && !r.cancel) TextButton(onClick = cancel) { Text(stringResource(R.string.b_cancel)) }
    }
}

@Composable
private fun runState(s: String): String = when (s) {
    "pending" -> stringResource(R.string.fleet_st_pending)
    "running" -> stringResource(R.string.fleet_st_running)
    "restarting", "rebooting" -> stringResource(R.string.fleet_st_restarting)
    "verifying" -> stringResource(R.string.fleet_st_verifying)
    "success" -> stringResource(R.string.fleet_st_success)
    "failed" -> stringResource(R.string.fleet_st_failed)
    "skipped" -> stringResource(R.string.fleet_st_skipped)
    "cancelled" -> stringResource(R.string.fleet_st_cancelled)
    else -> s
}

@Composable
private fun AddDialog(onDismiss: () -> Unit, add: (String, String) -> Unit) {
    var host by remember { mutableStateOf("") }
    var code by remember { mutableStateOf("") }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(stringResource(R.string.fleet_add)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(stringResource(R.string.fleet_add_help), style = MaterialTheme.typography.bodySmall)
                Field(stringResource(R.string.fleet_host), host, { host = it.trim() })
                Field(stringResource(R.string.fleet_code), code, { code = it.trim().uppercase() })
            }
        },
        confirmButton = { TextButton(enabled = validFleetHost(host) && code.isNotEmpty(), onClick = { add(host, code) }) { Text(stringResource(R.string.fleet_add)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
}

@Composable
private fun UpdateDialog(count: Int, onDismiss: () -> Unit, go: (String, String, Boolean, Boolean) -> Unit) {
    var kind by remember { mutableStateOf("strata") }
    var strategy by remember { mutableStateOf("rolling") }
    var reboot by remember { mutableStateOf(false) }
    var dry by remember { mutableStateOf(false) }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(stringResource(R.string.fleet_update_n, count)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    FilterChip(kind == "strata", { kind = "strata" }, label = { Text(stringResource(R.string.fleet_kind_strata)) })
                    FilterChip(kind == "system", { kind = "system" }, label = { Text(stringResource(R.string.fleet_kind_system)) })
                }
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    FilterChip(strategy == "rolling", { strategy = "rolling" }, label = { Text(stringResource(R.string.fleet_rolling)) })
                    FilterChip(strategy == "parallel", { strategy = "parallel" }, label = { Text(stringResource(R.string.fleet_parallel)) })
                }
                Text(stringResource(if (strategy == "rolling") R.string.fleet_rolling_help else R.string.fleet_parallel_help), style = MaterialTheme.typography.bodySmall)
                SwitchLine(stringResource(R.string.fleet_reboot), reboot, sub = stringResource(R.string.fleet_reboot_help)) { reboot = it }
                SwitchLine(stringResource(R.string.fleet_dry_run), dry, sub = stringResource(R.string.fleet_dry_help)) { dry = it }
            }
        },
        confirmButton = { TextButton(onClick = { go(kind, strategy, reboot, dry) }) { Text(stringResource(R.string.fleet_start)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
}

@Composable
private fun NasDetail(n: FleetNas, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val actions = rememberActions()
    var removing by remember { mutableStateOf(false) }
    val s = n.summary
    AreaScreen(n.name.ifEmpty { n.host }, onBack, actions = {
        if (!n.local) IconButton(onClick = { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(fleetWebUrl(n.host)))) }) {
            Icon(Icons.Default.OpenInBrowser, stringResource(R.string.fleet_open))
        }
        if (!n.local) IconButton(onClick = { removing = true }) { Icon(Icons.Default.Delete, stringResource(R.string.fleet_remove)) }
    }) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                if (n.error.isNotEmpty()) SectionCard { Text(n.error, color = MaterialTheme.colorScheme.error) }
                if (s != null) {
                    SectionCard(stringResource(R.string.fleet_system)) {
                        KeyValue(stringResource(R.string.fleet_version), s.version)
                        KeyValue(stringResource(R.string.fleet_uptime), stringResource(R.string.fleet_days, s.uptimeDays.toInt()))
                        s.sample?.let {
                            KeyValue(stringResource(R.string.fleet_cpu), "%.0f %%".format(it.cpu))
                            if (it.memTotal > 0) KeyValue(stringResource(R.string.fleet_memory), "${formatBytes(it.memUsed)} / ${formatBytes(it.memTotal)}")
                        }
                        KeyValue(stringResource(R.string.fleet_disks), s.disks.toString())
                        KeyValue(stringResource(R.string.fleet_containers), "${s.containers.running} / ${s.containers.total}")
                        s.ha?.let { KeyValue("HA", it.role + if (it.peers.isNotEmpty()) " · " + it.peers.joinToString(", ") else "") }
                    }
                    SectionCard(stringResource(R.string.fleet_updates)) {
                        KeyValue(stringResource(R.string.fleet_strata_update), s.updates.candidate.ifEmpty { stringResource(R.string.fleet_up_to_date) })
                        KeyValue(stringResource(R.string.fleet_system_updates), s.updates.system.toString())
                        if (s.updates.rebootRequired) Text(stringResource(R.string.fleet_reboot_required), color = MaterialTheme.colorScheme.error)
                        s.access?.let { KeyValue(stringResource(R.string.fleet_remote_updates), stringResource(if (it.updates) R.string.vpn_allowed else R.string.vpn_not_allowed)) }
                    }
                    if (s.alerts.isNotEmpty()) SectionCard(stringResource(R.string.status_alerts)) {
                        s.alerts.forEach { a -> Text(a.message, color = if (a.level == "error") MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurface) }
                    }
                    SectionCard(stringResource(R.string.status_storage)) {
                        s.volumes.forEach { v ->
                            Text("${v.name} · ${v.health}", style = MaterialTheme.typography.bodyLarge)
                            if (v.total > 0) LinearProgressIndicator(progress = { v.used.toFloat() / v.total }, Modifier.fillMaxWidth())
                            Text(stringResource(R.string.status_usage, formatBytes(v.used), formatBytes(v.total), formatBytes(v.free)), style = MaterialTheme.typography.bodySmall)
                        }
                        KeyValue(stringResource(R.string.fleet_backups), stringResource(R.string.fleet_backups_state, s.backups.ok, s.backups.failed))
                    }
                }
            }
        }
    }
    if (removing) Confirm(stringResource(R.string.fleet_remove), stringResource(R.string.fleet_remove_text, n.name), stringResource(R.string.fleet_remove),
        onConfirm = { actions.run(after = onBack) { it.removeFleetNas(n.id) } }, onDismiss = { removing = false })
}

/** The consoles that may watch this NAS, and a code to let a new console in. */
@Composable
private fun AccessScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.fleetManagers() }
    val actions = rememberActions()
    val res = LocalResources.current
    var code by remember { mutableStateOf<String?>(null) }
    AreaScreen(stringResource(R.string.fleet_access), onBack) { pad ->
        Refreshable(data, pad) { list ->
            ScrollColumn {
                SectionCard(stringResource(R.string.fleet_invite)) {
                    Text(stringResource(R.string.fleet_invite_help), style = MaterialTheme.typography.bodySmall)
                    code?.let { Text(it, style = MaterialTheme.typography.headlineMedium, fontFamily = FontFamily.Monospace) }
                    Button(onClick = { actions.run { code = it.fleetCode().code } }, enabled = !actions.busy) { Text(stringResource(R.string.fleet_new_code)) }
                }
                if (list.isEmpty()) Text(stringResource(R.string.fleet_no_managers), style = MaterialTheme.typography.bodySmall)
                list.forEach { m ->
                    var revoke by remember { mutableStateOf(false) }
                    SectionCard(m.name) {
                        if (m.lastSeen.isNotEmpty()) KeyValue(stringResource(R.string.fleet_last_seen), formatDate(m.lastSeen))
                        SwitchLine(stringResource(R.string.fleet_allow_updates), m.allowUpdates, enabled = !actions.busy) { on ->
                            actions.run(done = res.getString(R.string.b_saved), after = data.reload) { it.setFleetManager(m.id, on, m.allowReboot && on) }
                        }
                        SwitchLine(stringResource(R.string.fleet_allow_reboot), m.allowReboot, enabled = m.allowUpdates && !actions.busy) { on ->
                            actions.run(done = res.getString(R.string.b_saved), after = data.reload) { it.setFleetManager(m.id, m.allowUpdates, on) }
                        }
                        TextButton(onClick = { revoke = true }) { Text(stringResource(R.string.vpn_revoke), color = MaterialTheme.colorScheme.error) }
                    }
                    if (revoke) Confirm(stringResource(R.string.vpn_revoke), stringResource(R.string.fleet_revoke_text, m.name), stringResource(R.string.vpn_revoke),
                        onConfirm = { actions.run(after = data.reload) { it.removeFleetManager(m.id) } }, onDismiss = { revoke = false })
                }
            }
        }
    }
}
