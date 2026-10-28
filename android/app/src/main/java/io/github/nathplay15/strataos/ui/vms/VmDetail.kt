package io.github.nathplay15.strataos.ui.vms

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
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.Vm
import io.github.nathplay15.strataos.net.manage.VmList
import io.github.nathplay15.strataos.net.manage.VmSnapshot
import io.github.nathplay15.strataos.net.manage.createSnapshot
import io.github.nathplay15.strataos.net.manage.deleteSnapshot
import io.github.nathplay15.strataos.net.manage.deleteVm
import io.github.nathplay15.strataos.net.manage.revertSnapshot
import io.github.nathplay15.strataos.net.manage.vmAction
import io.github.nathplay15.strataos.net.manage.vmAutostart
import io.github.nathplay15.strataos.net.manage.vmIsos
import io.github.nathplay15.strataos.net.manage.vmMedia
import io.github.nathplay15.strataos.net.manage.vmResources
import io.github.nathplay15.strataos.net.manage.vmSnapshots
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Field
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

/** Snapshot names the NAS accepts. */
val snapNameRe = Regex("^[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}$")

@Composable
fun VmDetail(v: Vm, list: VmList, cpu: Int?, onBack: () -> Unit, open: (String) -> Unit, reload: () -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    var confirm by remember { mutableStateOf<String?>(null) }
    var deleting by remember { mutableStateOf(false) }
    var withDisks by remember { mutableStateOf(false) }
    val snaps = rememberData(v.name, v.snapshots) { it.vmSnapshots(v.name) }
    AreaScreen(v.name, onBack, actions = {
        if (v.running || v.paused) IconButton(onClick = { open("console:${v.name}") }) { Icon(Icons.Default.Terminal, stringResource(R.string.vm_console)) }
        IconButton(onClick = { deleting = true }) { Icon(Icons.Default.Delete, stringResource(R.string.vm_delete)) }
    }) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                SectionCard {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        VmState(v)
                        if (v.cloud.isNotEmpty()) StatusBadge(stringResource(R.string.vm_cloud_badge), Tone.Accent)
                        cpu?.takeIf { v.running }?.let { Text(stringResource(R.string.vm_cpu_use, it), style = MaterialTheme.typography.bodySmall) }
                    }
                    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        powerActions(v).forEach { (a, label) ->
                            OutlinedButton(enabled = !actions.busy, onClick = { if (a in confirmed) confirm = a else actions.run(after = reload) { it.vmAction(v.name, a) } }) {
                                Text(stringResource(label))
                            }
                        }
                        if (v.running || v.paused) Button(onClick = { open("console:${v.name}") }) { Text(stringResource(R.string.vm_console)) }
                    }
                    KeyValue(stringResource(R.string.vm_os), v.os.ifEmpty { "—" })
                    KeyValue(stringResource(R.string.vm_firmware), v.firmware.uppercase())
                    v.nics.forEach { n -> KeyValue(stringResource(R.string.vm_network), listOf(if (n.type == "bridge") n.source else "NAT", n.ip, n.mac).filter { it.isNotEmpty() }.joinToString(" · ")) }
                    v.disks.forEach { d -> KeyValue(if (d.device == "cdrom") "CD-ROM" else d.target, listOf(d.source.ifEmpty { "—" }, if (d.size > 0) formatBytes(d.size) else "").filter { it.isNotEmpty() }.joinToString(" · ")) }
                    if (v.dir.isNotEmpty()) KeyValue(stringResource(R.string.vm_folder), v.dir)
                }
                Resources(v, reload)
                Media(v, reload)
                SectionCard(stringResource(R.string.vm_start_with_nas)) {
                    SwitchLine(stringResource(R.string.vm_autostart), v.autostart, enabled = !v.template && !actions.busy) { on -> actions.run(after = reload) { it.vmAutostart(v.name, on) } }
                }
                Snapshots(v, snaps.value.orEmpty(), snaps.reload)
            }
        }
    }
    confirm?.let { a -> PowerConfirm(v.name, a, onDismiss = { confirm = null }) { actions.run(after = reload) { it.vmAction(v.name, a) } } }
    if (deleting) Confirm(stringResource(R.string.vm_delete), stringResource(R.string.vm_delete_text, v.name), stringResource(R.string.b_delete),
        onConfirm = { actions.run(done = res.getString(R.string.vm_deleted), after = onBack) { it.deleteVm(v.name, withDisks) } },
        onDismiss = { deleting = false }, typeToConfirm = v.name, extra = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Checkbox(withDisks, { withDisks = it })
                Text(stringResource(R.string.vm_delete_disks))
            }
            if (!v.off) Text(stringResource(R.string.vm_delete_running), color = MaterialTheme.colorScheme.error)
        })
}

@Composable
private fun Resources(v: Vm, reload: () -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    var cpus by remember(v.vcpus) { mutableStateOf(v.vcpus.toString()) }
    var mem by remember(v.memoryMb) { mutableStateOf(v.memoryMb.toString()) }
    SectionCard(stringResource(R.string.vm_resources_title)) {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Field(stringResource(R.string.vm_vcpus), cpus, { cpus = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true, enabled = v.off)
            Field(stringResource(R.string.vm_memory_mb), mem, { mem = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true, enabled = v.off)
        }
        if (!v.off) Text(stringResource(R.string.vm_resources_off), style = MaterialTheme.typography.bodySmall)
        val c = cpus.toIntOrNull() ?: 0
        val m = mem.toIntOrNull() ?: 0
        Button(enabled = v.off && c in 1..64 && m >= 256 && (c != v.vcpus || m != v.memoryMb) && !actions.busy, onClick = {
            actions.run(done = res.getString(R.string.b_saved), after = reload) { it.vmResources(v.name, c, m) }
        }) { Text(stringResource(R.string.b_save)) }
    }
}

@Composable
private fun Media(v: Vm, reload: () -> Unit) {
    if (v.disks.none { it.device == "cdrom" }) return
    val actions = rememberActions()
    val isos = rememberData(v.name) { it.vmIsos() }
    var iso by remember(v.iso) { mutableStateOf(v.iso) }
    SectionCard("CD-ROM") {
        val opts = listOf("" to stringResource(R.string.vm_no_media)) + isos.value.orEmpty().map { it to it.substringAfterLast('/') }
        Choice(stringResource(R.string.vm_iso), opts + listOfNotNull((v.iso to v.iso.substringAfterLast('/')).takeIf { v.iso.isNotEmpty() && opts.none { o -> o.first == v.iso } }), iso, { iso = it })
        Button(enabled = iso != v.iso && !actions.busy, onClick = { actions.run(after = reload) { it.vmMedia(v.name, iso) } }) {
            Text(stringResource(if (iso.isEmpty()) R.string.vm_eject else R.string.vm_insert))
        }
    }
}

@Composable
private fun Snapshots(v: Vm, list: List<VmSnapshot>, reload: () -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    var creating by remember { mutableStateOf(false) }
    var revert by remember { mutableStateOf<VmSnapshot?>(null) }
    var delete by remember { mutableStateOf<VmSnapshot?>(null) }
    SectionCard(stringResource(R.string.vm_snapshots)) {
        if (list.isEmpty()) Text(stringResource(R.string.vm_no_snapshots), style = MaterialTheme.typography.bodySmall)
        list.sortedByDescending { it.created }.forEach { s ->
            Column {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text(s.name + if (s.current) " ★" else "", style = MaterialTheme.typography.bodyLarge)
                        Text(listOf(formatDate(s.created), s.description, if (s.memory) res.getString(R.string.vm_snap_memory) else "").filter { it.isNotEmpty() }.joinToString(" · "),
                            style = MaterialTheme.typography.bodySmall)
                    }
                    TextButton(onClick = { revert = s }, enabled = !actions.busy) { Text(stringResource(R.string.vm_revert)) }
                    IconButton(onClick = { delete = s }, enabled = !actions.busy) { Icon(Icons.Default.Delete, stringResource(R.string.b_delete)) }
                }
                HorizontalDivider()
            }
        }
        OutlinedButton(onClick = { creating = true }, enabled = !v.template) { Text(stringResource(R.string.vm_snapshot_take)) }
    }
    if (creating) {
        var name by remember { mutableStateOf("snap-" + java.text.SimpleDateFormat("yyyyMMdd-HHmm", java.util.Locale.ROOT).format(java.util.Date())) }
        var desc by remember { mutableStateOf("") }
        AlertDialog(onDismissRequest = { creating = false }, title = { Text(stringResource(R.string.vm_snapshot_take)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Field(stringResource(R.string.cam_name), name, { name = it }, error = if (!snapNameRe.matches(name)) stringResource(R.string.vm_name_rule) else null)
                    Field(stringResource(R.string.vm_description), desc, { desc = it })
                    if (v.running) Text(stringResource(R.string.vm_snap_running), style = MaterialTheme.typography.bodySmall)
                }
            },
            confirmButton = {
                TextButton(enabled = snapNameRe.matches(name), onClick = {
                    creating = false
                    actions.run(done = res.getString(R.string.vm_snap_created), after = reload) { it.createSnapshot(v.name, name, desc) }
                }) { Text(stringResource(R.string.vm_create_btn)) }
            },
            dismissButton = { TextButton(onClick = { creating = false }) { Text(stringResource(R.string.b_cancel)) } })
    }
    revert?.let { s ->
        Confirm(stringResource(R.string.vm_revert), stringResource(R.string.vm_revert_text, v.name, s.name), stringResource(R.string.vm_revert),
            onConfirm = { actions.run(after = reload) { it.revertSnapshot(v.name, s.name) } }, onDismiss = { revert = null }, typeToConfirm = s.name)
    }
    delete?.let { s ->
        Confirm(stringResource(R.string.vm_snap_delete), stringResource(R.string.vm_snap_delete_text, s.name), stringResource(R.string.b_delete),
            onConfirm = { actions.run(after = reload) { it.deleteSnapshot(v.name, s.name) } }, onDismiss = { delete = null })
    }
}

