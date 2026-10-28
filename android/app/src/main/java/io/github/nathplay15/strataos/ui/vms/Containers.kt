package io.github.nathplay15.strataos.ui.vms

import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Inventory2
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.Container
import io.github.nathplay15.strataos.net.manage.LxcSettings
import io.github.nathplay15.strataos.net.manage.LxcSnapshot
import io.github.nathplay15.strataos.net.manage.LxcStats
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.lxcAction
import io.github.nathplay15.strataos.net.manage.lxcActions
import io.github.nathplay15.strataos.net.manage.lxcBackup
import io.github.nathplay15.strataos.net.manage.lxcBackups
import io.github.nathplay15.strataos.net.manage.lxcClone
import io.github.nathplay15.strataos.net.manage.lxcCpuPercent
import io.github.nathplay15.strataos.net.manage.lxcDelete
import io.github.nathplay15.strataos.net.manage.lxcDeleteSnapshot
import io.github.nathplay15.strataos.net.manage.lxcRename
import io.github.nathplay15.strataos.net.manage.lxcRestore
import io.github.nathplay15.strataos.net.manage.lxcSnapshot
import io.github.nathplay15.strataos.net.manage.lxcSnapshots
import io.github.nathplay15.strataos.net.manage.lxcStats
import io.github.nathplay15.strataos.net.manage.lxcUpdate
import io.github.nathplay15.strataos.net.manage.settingsOf
import io.github.nathplay15.strataos.net.manage.validCidr
import io.github.nathplay15.strataos.net.manage.validLxcName
import io.github.nathplay15.strataos.ui.FolderPicker
import io.github.nathplay15.strataos.ui.adminkit.Actions
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate

// LXC containers in the Virtual machines area: card of the list, detail
// screen (summary, resources and limits, network, folders, snapshots,
// backups, options).

@Composable
fun ContainerState(c: Container) {
    val (label, tone) = when {
        c.missing -> R.string.ct_missing to Tone.Bad
        c.running -> R.string.vm_running to Tone.Ok
        c.stopped -> R.string.vm_off to Tone.Neutral
        c.state == "frozen" -> R.string.vm_paused to Tone.Warn
        else -> 0 to Tone.Warn
    }
    StatusBadge(if (label == 0) c.state else stringResource(label), tone)
}

@Composable
private fun actionLabel(a: String) = stringResource(when (a) {
    "start" -> R.string.vm_start
    "stop" -> R.string.vm_shutdown
    "restart" -> R.string.vm_reboot
    else -> R.string.vm_force_off
})

/** Actions that interrupt the container ask first. */
@Composable
private fun ActionConfirm(c: Container, a: String, actions: Actions, reload: () -> Unit, onDismiss: () -> Unit) {
    Confirm(actionLabel(a), stringResource(if (a == "kill") R.string.vm_force_off_text else if (a == "restart") R.string.vm_reboot_text else R.string.vm_shutdown_text, c.name),
        actionLabel(a), { actions.run(after = reload) { it.lxcAction(c.name, a) } }, onDismiss, destructive = a == "kill", typeToConfirm = if (a == "kill") c.name else null)
}

@Composable
fun ContainerCard(c: Container, actions: Actions, go: (String) -> Unit, reload: () -> Unit) {
    var confirm by remember { mutableStateOf<String?>(null) }
    Card(Modifier.fillMaxWidth().clickable { go("ct:${c.name}") }) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Default.Inventory2, null, Modifier.padding(end = 8.dp), tint = MaterialTheme.colorScheme.primary)
                Text(c.name, Modifier.weight(1f), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                StatusBadge(stringResource(R.string.ct_badge), Tone.Accent)
                Box(Modifier.padding(start = 6.dp)) { ContainerState(c) }
            }
            Text(listOf("${c.image.distro} ${c.image.release}", limitsText(c)).filter { it.isNotBlank() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
            if (c.ips.isNotEmpty()) Text(c.ips.joinToString(", "), style = MaterialTheme.typography.bodySmall)
            if (c.running && c.memoryBytes > 0) Text(stringResource(R.string.ct_mem_use, formatBytes(c.memoryBytes)), style = MaterialTheme.typography.labelSmall)
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (c.stopped && !c.missing) IconButton(onClick = { actions.run(after = reload) { it.lxcAction(c.name, "start") } }, enabled = !actions.busy) {
                    Icon(Icons.Default.PlayArrow, stringResource(R.string.vm_start))
                }
                if (c.running) {
                    IconButton(onClick = { confirm = "stop" }, enabled = !actions.busy) { Icon(Icons.Default.Stop, stringResource(R.string.vm_shutdown)) }
                    IconButton(onClick = { go("term:${c.name}") }) { Icon(Icons.Default.Terminal, stringResource(R.string.ct_terminal)) }
                }
            }
        }
    }
    confirm?.let { a -> ActionConfirm(c, a, actions, reload) { confirm = null } }
}

@Composable
private fun limitsText(c: Container): String = listOfNotNull(
    if (c.cpus > 0) stringResource(R.string.ct_cpus_n, c.cpus.fmt()) else null,
    if (c.memoryMb > 0) formatBytes(c.memoryMb * 1024L * 1024) else null,
    if (c.storage.sizeGb > 0) "${c.storage.sizeGb} GB" else null,
).joinToString(" · ")

private fun Double.fmt() = if (this == Math.floor(this)) toInt().toString() else toString()

@Composable
fun ContainerDetail(c: Container, bridges: List<String>, onBack: () -> Unit, open: (String) -> Unit, reload: () -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    var confirm by remember { mutableStateOf<String?>(null) }
    var menu by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    var naming by remember { mutableStateOf<String?>(null) } // "clone" or "rename"
    var job by remember { mutableStateOf<NasJob?>(null) }
    val save: (LxcSettings) -> Unit = { s ->
        actions.run(after = reload) { api ->
            val r = api.lxcUpdate(c.name, s)
            actions.say(res.getString(if (r.restart) R.string.ct_saved_restart else R.string.b_saved))
        }
    }
    AreaScreen(c.name, onBack, subtitle = c.description.ifEmpty { null }, actions = {
        if (c.running) IconButton(onClick = { open("term:${c.name}") }) { Icon(Icons.Default.Terminal, stringResource(R.string.ct_terminal)) }
        Box {
            IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.b_more)) }
            DropdownMenu(menu, { menu = false }) {
                DropdownMenuItem({ Text(stringResource(R.string.ct_clone)) }, { menu = false; naming = "clone" })
                DropdownMenuItem({ Text(stringResource(R.string.ct_rename)) }, { menu = false; naming = "rename" }, enabled = c.stopped)
                DropdownMenuItem({ Text(stringResource(R.string.ct_delete)) }, { menu = false; deleting = true }, leadingIcon = { Icon(Icons.Default.Delete, null) })
            }
        }
    }) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                SectionCard {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        ContainerState(c)
                        if (c.privileged) StatusBadge(stringResource(R.string.ct_privileged_badge), Tone.Warn)
                    }
                    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        lxcActions(c).forEach { a ->
                            OutlinedButton(enabled = !actions.busy, onClick = { if (a == "start") actions.run(after = reload) { it.lxcAction(c.name, a) } else confirm = a }) { Text(actionLabel(a)) }
                        }
                        if (c.running) {
                            Button(onClick = { open("term:${c.name}") }) { Text(stringResource(R.string.ct_terminal)) }
                        }
                    }
                    if (c.missing) Text(stringResource(R.string.ct_missing_text), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                    KeyValue(stringResource(R.string.ct_image), "${c.image.distro} ${c.image.release} (${c.image.arch})")
                    if (c.ips.isNotEmpty()) KeyValue(stringResource(R.string.ct_addresses), c.ips.joinToString(", "))
                    KeyValue(stringResource(R.string.ct_storage), "${backendLabel(c.storage.backend)} · ${c.storage.location}")
                    if (c.created.isNotEmpty()) KeyValue(stringResource(R.string.ct_created), formatDate(c.created))
                }
                Resources(c, save, actions.busy)
                var net by remember(c.network) { mutableStateOf(c.network) }
                SectionCard(stringResource(R.string.ct_network)) {
                    NetworkEditor(net, bridges) { net = it }
                    Button(enabled = net != c.network && validCidr(net.address) && !actions.busy, onClick = { save(settingsOf(c).copy(network = net)) }) { Text(stringResource(R.string.b_save)) }
                }
                var mounts by remember(c.mounts) { mutableStateOf(settingsOf(c).mounts) }
                SectionCard(stringResource(R.string.ct_mounts)) {
                    MountsEditor(mounts, c.privileged, c.mapUsers + c.mapGroups) { mounts = it }
                    Button(enabled = mounts != settingsOf(c).mounts && !actions.busy, onClick = { save(settingsOf(c).copy(mounts = mounts)) }) { Text(stringResource(R.string.b_save)) }
                }
                Snapshots(c, actions) { job = it }
                Backups(c, actions) { job = it }
                Options(c, save, actions.busy)
            }
        }
    }
    confirm?.let { a -> ActionConfirm(c, a, actions, reload) { confirm = null } }
    if (deleting) Confirm(stringResource(R.string.ct_delete), stringResource(R.string.ct_delete_text, c.name), stringResource(R.string.b_delete),
        onConfirm = { actions.run(done = res.getString(R.string.ct_deleted), after = onBack) { it.lxcDelete(c.name) } }, onDismiss = { deleting = false }, typeToConfirm = c.name,
        extra = { if (!c.stopped) Text(stringResource(R.string.ct_delete_running), color = MaterialTheme.colorScheme.error) })
    naming?.let { what ->
        var name by remember { mutableStateOf(if (what == "rename") c.name else c.name + "-copy") }
        AlertDialog(onDismissRequest = { naming = null }, title = { Text(stringResource(if (what == "clone") R.string.ct_clone else R.string.ct_rename)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(stringResource(if (what == "clone") R.string.ct_clone_help else R.string.ct_rename_help), style = MaterialTheme.typography.bodySmall)
                    Field(stringResource(R.string.cam_name), name, { name = it.lowercase() }, error = if (!validLxcName(name)) stringResource(R.string.ct_name_rule) else null)
                }
            },
            confirmButton = {
                TextButton(enabled = validLxcName(name) && name != c.name, onClick = {
                    naming = null
                    if (what == "clone") actions.run { job = it.lxcClone(c.name, name) }
                    else actions.run(after = onBack) { it.lxcRename(c.name, name) }
                }) { Text(stringResource(R.string.b_ok)) }
            },
            dismissButton = { TextButton(onClick = { naming = null }) { Text(stringResource(R.string.b_cancel)) } })
    }
    job?.let { j -> JobDialog(j) { job = null; reload() } }
}

@Composable
private fun Resources(c: Container, save: (LxcSettings) -> Unit, busy: Boolean) {
    var prev by remember { mutableStateOf<LxcStats?>(null) }
    var cpu by remember { mutableStateOf<Double?>(null) }
    val stats = rememberData(c.name, every = 2000) { api ->
        api.lxcStats(c.name).also { s ->
            cpu = lxcCpuPercent(prev, s)
            prev = s
        }
    }
    var cpus by remember(c.cpus) { mutableStateOf(if (c.cpus > 0) c.cpus.fmt() else "0") }
    var mem by remember(c.memoryMb) { mutableStateOf(c.memoryMb.toString()) }
    var swap by remember(c.swapMb) { mutableStateOf(c.swapMb.toString()) }
    var size by remember(c.storage.sizeGb) { mutableStateOf(c.storage.sizeGb.toString()) }
    SectionCard(stringResource(R.string.ct_resources)) {
        val s = stats.value
        if (s != null && c.running) {
            val max = (if (s.cpuLimit > 0) s.cpuLimit else s.cores.coerceAtLeast(1).toDouble()) * 100
            cpu?.let {
                Text(stringResource(R.string.ct_cpu_use, it.toInt(), max.toInt()), style = MaterialTheme.typography.bodySmall)
                LinearProgressIndicator(progress = { (it / max).toFloat().coerceIn(0f, 1f) }, Modifier.fillMaxWidth())
            }
            Text(stringResource(R.string.ct_mem_line, formatBytes(s.memory), if (s.memoryMax > 0) formatBytes(s.memoryMax) else "—", s.pids.toInt()), style = MaterialTheme.typography.bodySmall)
            if (s.memoryMax > 0) LinearProgressIndicator(progress = { (s.memory.toFloat() / s.memoryMax).coerceIn(0f, 1f) }, Modifier.fillMaxWidth())
            Text(stringResource(R.string.ct_net_line, formatBytes(s.netRx), formatBytes(s.netTx)), style = MaterialTheme.typography.bodySmall)
        } else Text(stringResource(R.string.ct_stats_stopped), style = MaterialTheme.typography.bodySmall)
        if (s != null && s.diskUsed > 0) Text(stringResource(R.string.ct_disk_line, formatBytes(s.diskUsed), if (s.diskLimit > 0) formatBytes(s.diskLimit) else "—"), style = MaterialTheme.typography.bodySmall)
        HorizontalDivider()
        Text(stringResource(R.string.ct_limits), style = MaterialTheme.typography.titleSmall)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Field(stringResource(R.string.ct_cpus), cpus, { cpus = it.filter { ch -> ch.isDigit() || ch == '.' } }, Modifier.weight(1f), number = true)
            Field(stringResource(R.string.ct_memory), mem, { mem = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Field(stringResource(R.string.ct_swap), swap, { swap = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true, enabled = (mem.toIntOrNull() ?: 0) > 0)
            Field(stringResource(R.string.ct_disk), size, { size = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true, enabled = c.storage.backend != "dir",
                help = if (c.storage.backend == "image") stringResource(R.string.ct_disk_grow) else null)
        }
        Text(stringResource(R.string.ct_limits_help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        val ok = cpus.toDoubleOrNull() != null && mem.toIntOrNull() != null && swap.toIntOrNull() != null && size.toIntOrNull() != null
        Button(enabled = ok && !busy, onClick = {
            save(settingsOf(c).copy(cpus = cpus.toDouble(), memoryMb = mem.toInt(), swapMb = swap.toInt(), sizeGb = size.toInt()))
        }) { Text(stringResource(R.string.ct_apply)) }
    }
}

@Composable
private fun Snapshots(c: Container, actions: Actions, onJob: (NasJob) -> Unit) {
    val res = LocalResources.current
    val snaps = rememberData(c.name, c.snapshots, every = 5000) { it.lxcSnapshots(c.name) }
    var creating by remember { mutableStateOf(false) }
    var restore by remember { mutableStateOf<LxcSnapshot?>(null) }
    var delete by remember { mutableStateOf<LxcSnapshot?>(null) }
    SectionCard(stringResource(R.string.vm_snapshots)) {
        val list = snaps.value.orEmpty()
        if (list.isEmpty()) Text(stringResource(R.string.vm_no_snapshots), style = MaterialTheme.typography.bodySmall)
        list.sortedByDescending { it.created }.forEach { s ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(s.name, style = MaterialTheme.typography.bodyLarge)
                    Text(listOf(formatDate(s.created), s.description, if (s.running) res.getString(R.string.ct_snap_running) else "").filter { it.isNotEmpty() }.joinToString(" · "),
                        style = MaterialTheme.typography.bodySmall)
                }
                TextButton(onClick = { restore = s }, enabled = !actions.busy) { Text(stringResource(R.string.vm_revert)) }
                IconButton(onClick = { delete = s }, enabled = !actions.busy) { Icon(Icons.Default.Delete, stringResource(R.string.b_delete)) }
            }
            HorizontalDivider()
        }
        OutlinedButton(onClick = { creating = true }) { Text(stringResource(R.string.vm_snapshot_take)) }
    }
    if (creating) {
        var name by remember { mutableStateOf("snap-" + java.text.SimpleDateFormat("yyyyMMdd-HHmm", java.util.Locale.ROOT).format(java.util.Date())) }
        var desc by remember { mutableStateOf("") }
        AlertDialog(onDismissRequest = { creating = false }, title = { Text(stringResource(R.string.vm_snapshot_take)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Field(stringResource(R.string.cam_name), name, { name = it }, error = if (!snapNameRe.matches(name)) stringResource(R.string.vm_name_rule) else null)
                    Field(stringResource(R.string.vm_description), desc, { desc = it })
                }
            },
            confirmButton = {
                TextButton(enabled = snapNameRe.matches(name), onClick = {
                    creating = false
                    actions.run(after = snaps.reload) { onJob(it.lxcSnapshot(c.name, name, desc)) }
                }) { Text(stringResource(R.string.vm_create_btn)) }
            },
            dismissButton = { TextButton(onClick = { creating = false }) { Text(stringResource(R.string.b_cancel)) } })
    }
    restore?.let { s ->
        Confirm(stringResource(R.string.vm_revert), stringResource(R.string.ct_restore_text, c.name, s.name), stringResource(R.string.vm_revert),
            onConfirm = { actions.run { onJob(it.lxcRestore(c.name, s.name)) } }, onDismiss = { restore = null }, typeToConfirm = s.name)
    }
    delete?.let { s ->
        Confirm(stringResource(R.string.vm_snap_delete), stringResource(R.string.vm_snap_delete_text, s.name), stringResource(R.string.b_delete),
            onConfirm = { actions.run(after = snaps.reload) { it.lxcDeleteSnapshot(c.name, s.name) } }, onDismiss = { delete = null })
    }
}

@Composable
private fun Backups(c: Container, actions: Actions, onJob: (NasJob) -> Unit) {
    var dir by remember { mutableStateOf("") }
    var mode by remember { mutableStateOf("") }
    var keep by remember { mutableStateOf("5") }
    var picking by remember { mutableStateOf(false) }
    val list = rememberData(c.name, dir, every = if (dir.isEmpty()) 0 else 5000) { if (dir.isEmpty()) emptyList() else it.lxcBackups(c.name, dir) }
    SectionCard(stringResource(R.string.ct_backups)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(dir.removePrefix("/srv/strata").ifEmpty { stringResource(R.string.ct_backup_no_folder) }, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
            OutlinedButton(onClick = { picking = true }) { Text(stringResource(R.string.ct_backup_folder)) }
        }
        Choice(stringResource(R.string.ct_backup_mode), listOf(
            "" to stringResource(if (c.storage.backend == "dir") R.string.ct_backup_live else R.string.ct_backup_snapshot),
            "stop" to stringResource(R.string.ct_backup_stop), "live" to stringResource(R.string.ct_backup_live),
        ), mode, { mode = it })
        Field(stringResource(R.string.ct_backup_keep), keep, { keep = it.filter(Char::isDigit) }, number = true)
        Button(enabled = dir.isNotEmpty() && (keep.toIntOrNull() ?: 0) in 1..1000 && !actions.busy, onClick = {
            actions.run(after = list.reload) { onJob(it.lxcBackup(c.name, dir, mode, keep.toInt())) }
        }) { Text(stringResource(R.string.ct_backup_now)) }
        list.value.orEmpty().sortedByDescending { it.created }.forEach { a ->
            Column(Modifier.padding(vertical = 2.dp)) {
                Text(a.path.substringAfterLast('/'), style = MaterialTheme.typography.bodyMedium)
                Text("${formatDate(a.created)} · ${formatBytes(a.size)}", style = MaterialTheme.typography.bodySmall)
            }
        }
        Text(stringResource(R.string.ct_backup_help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
    if (picking) FolderPicker(stringResource(R.string.ct_backup_folder), start = dir, onCancel = { picking = false }, onPick = { dir = it; picking = false })
}

@Composable
private fun Options(c: Container, save: (LxcSettings) -> Unit, busy: Boolean) {
    var desc by remember(c.description) { mutableStateOf(c.description) }
    var features by remember(c.features) { mutableStateOf(c.features) }
    var auto by remember(c.autostart) { mutableStateOf(c.autostart) }
    var order by remember(c.autostartOrder) { mutableStateOf(c.autostartOrder.toString()) }
    var delay by remember(c.autostartDelay) { mutableStateOf(c.autostartDelay.toString()) }
    SectionCard(stringResource(R.string.ct_options)) {
        Field(stringResource(R.string.vm_description), desc, { desc = it })
        FeaturesEditor(features, c.privileged) { features = it }
        SwitchLine(stringResource(R.string.ct_autostart), auto) { auto = it }
        if (auto) Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Field(stringResource(R.string.ct_order), order, { order = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
            Field(stringResource(R.string.ct_delay), delay, { delay = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
        }
        Button(enabled = !busy, onClick = {
            save(settingsOf(c).copy(description = desc, features = features, autostart = auto, autostartOrder = order.toIntOrNull() ?: 0, autostartDelay = delay.toIntOrNull() ?: 0))
        }) { Text(stringResource(R.string.b_save)) }
    }
}
