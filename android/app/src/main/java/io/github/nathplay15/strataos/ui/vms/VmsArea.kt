package io.github.nathplay15.strataos.ui.vms

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.LxcList
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.lxc
import io.github.nathplay15.strataos.net.manage.Vm
import io.github.nathplay15.strataos.net.manage.VmList
import io.github.nathplay15.strataos.net.manage.installComponent
import io.github.nathplay15.strataos.net.manage.vmAction
import io.github.nathplay15.strataos.net.manage.vms
import io.github.nathplay15.strataos.ui.adminkit.Actions
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes

// Virtual machines: list with state and resources, power actions, details
// (snapshots, resources, CD-ROM, deletion), creation wizard and the console.
// Routes: "list", "vm:<name>", "console:<name>", "new"; LXC containers (shown
// with the VMs, like the web interface): "ct:<name>", "ctnew", "term:<name>".

@Composable
fun VmsArea(back: () -> Unit) {
    var route by rememberSaveable { mutableStateOf("list") }
    val (kind, name) = route.split(":", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
    val polling = kind == "list" || kind == "vm" // not behind the console
    // CPU use: the CPU time of each VM between two refreshes.
    val cpu = remember { mutableStateMapOf<String, Pair<Long, Long>>() }
    val cpuPct = remember { mutableStateMapOf<String, Int>() }
    val data = rememberData(polling, every = if (polling) 5000 else 0) { api ->
        api.vms().also { l ->
            val now = System.nanoTime()
            l.stats.forEach { (n, s) ->
                val t = s.getOrNull(0) ?: return@forEach
                cpu[n]?.let { (pt, pn) -> if (now > pn) cpuPct[n] = (cpuPercent(t - pt, now - pn, l.vms.firstOrNull { it.name == n }?.vcpus ?: 1)) }
                cpu[n] = t to now
            }
        }
    }
    val cts = rememberData(polling || kind == "ct", every = if (polling || kind == "ct") 5000 else 0) { api -> api.lxc() }
    val home = { route = "list"; data.reload(); cts.reload() }
    val vm = data.value?.vms?.firstOrNull { it.name == name }
    val ct = cts.value?.containers?.firstOrNull { it.name == name }
    when {
        kind == "console" && name.isNotEmpty() -> VncScreen(name, onBack = { route = "vm:$name" })
        kind == "new" -> VmCreate(data.value, onBack = home)
        kind == "vm" && vm != null -> VmDetail(vm, data.value!!, cpuPct[vm.name], onBack = home, open = { route = it }, reload = data.reload)
        kind == "ctnew" -> ContainerCreate(cts.value?.bridges.orEmpty(), onBack = home)
        kind == "ct" && ct != null -> ContainerDetail(ct, cts.value!!.bridges, onBack = home, open = { route = it }, reload = cts.reload)
        kind == "term" && name.isNotEmpty() -> ContainerTerminal(name, onBack = { route = "ct:$name" })
        else -> VmList(data, cts, cpuPct, back) { route = it }
    }
}

/** CPU use in percent of the VM's vCPUs from its CPU time over an interval. */
fun cpuPercent(cpuNs: Long, wallNs: Long, vcpus: Int): Int =
    if (wallNs <= 0 || cpuNs < 0) 0 else ((cpuNs * 100.0 / wallNs) / vcpus.coerceAtLeast(1)).toInt().coerceIn(0, 100)

@Composable
fun VmState(v: Vm) {
    val (label, tone) = when {
        v.template -> R.string.vm_template to Tone.Accent
        v.running -> R.string.vm_running to Tone.Ok
        v.paused -> R.string.vm_paused to Tone.Warn
        v.off -> R.string.vm_off to Tone.Neutral
        else -> 0 to Tone.Warn
    }
    StatusBadge(if (label == 0) v.state else stringResource(label), tone)
}

@Composable
private fun VmList(
    data: io.github.nathplay15.strataos.ui.adminkit.Data<VmList>, cts: io.github.nathplay15.strataos.ui.adminkit.Data<LxcList>,
    cpu: Map<String, Int>, back: () -> Unit, go: (String) -> Unit,
) {
    val actions = rememberActions()
    var job by remember { mutableStateOf<NasJob?>(null) }
    var filter by rememberSaveable { mutableStateOf("all") }
    var adding by remember { mutableStateOf(false) }
    val lxc = cts.value
    AreaScreen(stringResource(R.string.area_vms), back, fab = {
        val vmsOk = data.value?.installed == true
        val ctOk = lxc?.installed == true
        if (vmsOk || ctOk) Box {
            ExtendedFloatingActionButton(onClick = {
                when {
                    filter == "vms" || !ctOk -> go("new")
                    filter == "cts" || !vmsOk -> go("ctnew")
                    else -> adding = true
                }
            }, icon = { Icon(Icons.Default.Add, null) }, text = { Text(stringResource(R.string.vm_create)) })
            DropdownMenu(adding, { adding = false }) {
                DropdownMenuItem({ Text(stringResource(R.string.ct_new_vm)) }, { adding = false; go("new") })
                DropdownMenuItem({ Text(stringResource(R.string.ct_new)) }, { adding = false; go("ctnew") })
            }
        }
    }) { pad ->
        Refreshable(data, pad) { l ->
            val showVms = filter != "cts"
            val showCts = filter != "vms"
            LazyVerticalGrid(GridCells.Adaptive(340.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(12.dp, 12.dp, 12.dp, 88.dp),
                horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        listOf("all" to R.string.ct_filter_all, "vms" to R.string.ct_filter_vms, "cts" to R.string.ct_filter_cts).forEach { (f, label) ->
                            FilterChip(filter == f, { filter = f }, { Text(stringResource(label)) })
                        }
                    }
                }
                if (showVms) {
                    if (!l.installed) item(span = { GridItemSpan(maxLineSpan) }) {
                        Card { Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text(stringResource(R.string.vm_not_installed))
                            Button(onClick = { actions.run { job = it.installComponent("vms") } }, enabled = !actions.busy) { Text(stringResource(R.string.vm_install)) }
                        } }
                    } else {
                        if (!l.kvm) item(span = { GridItemSpan(maxLineSpan) }) { Text(stringResource(R.string.vm_no_kvm), color = MaterialTheme.colorScheme.error) }
                        if (l.vms.isEmpty() && filter == "vms") item(span = { GridItemSpan(maxLineSpan) }) { Text(stringResource(R.string.vm_none), color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        items(l.vms, key = { "vm:" + it.name }) { v -> VmCard(v, l, cpu[v.name], actions, go, data.reload) }
                    }
                }
                if (showCts && lxc != null) {
                    if (!lxc.installed) item(span = { GridItemSpan(maxLineSpan) }) {
                        Card { Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text(stringResource(R.string.ct_not_installed))
                            Button(onClick = { actions.run { job = it.installComponent("lxc") } }, enabled = !actions.busy) { Text(stringResource(R.string.ct_install)) }
                        } }
                    } else {
                        if (lxc.containers.isEmpty() && filter == "cts") item(span = { GridItemSpan(maxLineSpan) }) { Text(stringResource(R.string.ct_none), color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        items(lxc.containers, key = { "ct:" + it.name }) { c -> ContainerCard(c, actions, go, cts.reload) }
                    }
                }
            }
        }
    }
    job?.let { j -> JobDialog(j) { job = null; data.reload(); cts.reload() } }
}

@Composable
private fun VmCard(v: Vm, l: VmList, cpu: Int?, actions: Actions, go: (String) -> Unit, reload: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf<String?>(null) }
    Card(Modifier.fillMaxWidth().clickable { go("vm:${v.name}") }) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(v.name, Modifier.weight(1f), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                VmState(v)
            }
            Text(stringResource(R.string.vm_resources, v.vcpus, formatBytes(v.memoryMb * 1024L * 1024), formatBytes(v.diskBytes)), style = MaterialTheme.typography.bodySmall)
            v.nics.mapNotNull { it.ip.ifEmpty { null } }.takeIf { it.isNotEmpty() }?.let { Text(it.joinToString(", "), style = MaterialTheme.typography.bodySmall) }
            if (v.running) {
                val mem = l.stats[v.name]?.getOrNull(1)
                cpu?.let {
                    Text(stringResource(R.string.vm_cpu_use, it), style = MaterialTheme.typography.labelSmall)
                    LinearProgressIndicator(progress = { it / 100f }, Modifier.fillMaxWidth())
                }
                if (mem != null && mem > 0) Text(stringResource(R.string.vm_mem_use, formatBytes(mem * 1024), formatBytes(v.memoryMb * 1024L * 1024)), style = MaterialTheme.typography.labelSmall)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (v.off && !v.template) IconButton(onClick = { actions.run(after = reload) { it.vmAction(v.name, "start") } }, enabled = !actions.busy) {
                    Icon(Icons.Default.PlayArrow, stringResource(R.string.vm_start))
                }
                if (v.running) IconButton(onClick = { confirm = "shutdown" }, enabled = !actions.busy) { Icon(Icons.Default.Stop, stringResource(R.string.vm_shutdown)) }
                if (v.running || v.paused) IconButton(onClick = { go("console:${v.name}") }) { Icon(Icons.Default.Terminal, stringResource(R.string.vm_console)) }
                Box(Modifier.weight(1f))
                Box {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.b_more)) }
                    DropdownMenu(menu, { menu = false }) {
                        powerActions(v).forEach { (a, label) ->
                            DropdownMenuItem(text = { Text(stringResource(label)) }, onClick = {
                                menu = false
                                if (a in confirmed) confirm = a else actions.run(after = reload) { it.vmAction(v.name, a) }
                            })
                        }
                    }
                }
            }
        }
    }
    confirm?.let { a -> PowerConfirm(v.name, a, onDismiss = { confirm = null }) { actions.run(after = reload) { it.vmAction(v.name, a) } } }
}

/** The actions that interrupt the guest ask first. */
val confirmed = setOf("shutdown", "destroy", "reboot")

/** The power actions offered for a VM in its current state. */
fun powerActions(v: Vm): List<Pair<String, Int>> = when {
    v.template -> emptyList()
    v.running -> listOf("shutdown" to R.string.vm_shutdown, "reboot" to R.string.vm_reboot, "suspend" to R.string.vm_pause, "destroy" to R.string.vm_force_off)
    v.paused -> listOf("resume" to R.string.vm_resume, "destroy" to R.string.vm_force_off)
    v.off -> listOf("start" to R.string.vm_start)
    else -> listOf("destroy" to R.string.vm_force_off)
}

@Composable
fun PowerConfirm(vm: String, action: String, onDismiss: () -> Unit, go: () -> Unit) {
    val (title, text) = when (action) {
        "destroy" -> R.string.vm_force_off to R.string.vm_force_off_text
        "reboot" -> R.string.vm_reboot to R.string.vm_reboot_text
        else -> R.string.vm_shutdown to R.string.vm_shutdown_text
    }
    Confirm(stringResource(title), stringResource(text, vm), stringResource(title), go, onDismiss, destructive = action == "destroy",
        typeToConfirm = if (action == "destroy") vm else null)
}
