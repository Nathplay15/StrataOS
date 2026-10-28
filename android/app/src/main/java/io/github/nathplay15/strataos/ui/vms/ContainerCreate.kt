package io.github.nathplay15.strataos.ui.vms

import androidx.compose.foundation.BorderStroke
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
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.LxcCreateReq
import io.github.nathplay15.strataos.net.manage.LxcFeatures
import io.github.nathplay15.strataos.net.manage.LxcImageRef
import io.github.nathplay15.strataos.net.manage.LxcMount
import io.github.nathplay15.strataos.net.manage.LxcNetwork
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.filterImages
import io.github.nathplay15.strataos.net.manage.lxcCreate
import io.github.nathplay15.strataos.net.manage.lxcStorage
import io.github.nathplay15.strataos.net.manage.lxcTemplates
import io.github.nathplay15.strataos.net.manage.validCidr
import io.github.nathplay15.strataos.net.manage.validLxcName
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate

/** Wizard of a new container: image, resources and storage, network, folders and options. */
@Composable
fun ContainerCreate(bridges: List<String>, onBack: () -> Unit) {
    val actions = rememberActions()
    var step by rememberSaveable { mutableIntStateOf(0) }
    var refresh by remember { mutableIntStateOf(0) }
    val images = rememberData(refresh) { it.lxcTemplates(refresh > 0) }
    val locs = rememberData(Unit) { it.lxcStorage() }
    var search by rememberSaveable { mutableStateOf("") }
    var all by rememberSaveable { mutableStateOf(false) }
    var distro by rememberSaveable { mutableStateOf("debian") }
    var release by rememberSaveable { mutableStateOf("trixie") }
    var name by rememberSaveable { mutableStateOf("") }
    var desc by rememberSaveable { mutableStateOf("") }
    var location by rememberSaveable { mutableStateOf("") }
    var backend by rememberSaveable { mutableStateOf("image") }
    var size by rememberSaveable { mutableStateOf("8") }
    var cpus by rememberSaveable { mutableStateOf("2") }
    var mem by rememberSaveable { mutableStateOf("1024") }
    var swap by rememberSaveable { mutableStateOf("512") }
    var net by remember { mutableStateOf(LxcNetwork()) }
    var mounts by remember { mutableStateOf(emptyList<LxcMount>()) }
    var features by remember { mutableStateOf(LxcFeatures()) }
    var password by remember { mutableStateOf("") }
    var start by rememberSaveable { mutableStateOf(true) }
    var autostart by rememberSaveable { mutableStateOf(false) }
    var privileged by rememberSaveable { mutableStateOf(false) }
    var job by remember { mutableStateOf<NasJob?>(null) }
    LaunchedEffect(locs.value) {
        val l = locs.value
        if (location.isEmpty() && !l.isNullOrEmpty()) {
            location = l[0].path
            backend = l[0].backends.firstOrNull() ?: "image"
        }
    }
    val steps = listOf(R.string.ct_step_image, R.string.ct_step_resources, R.string.ct_step_network, R.string.ct_step_options)
    val nameOk = validLxcName(name)
    val numbersOk = cpus.toDoubleOrNull() != null && mem.toIntOrNull() != null && swap.toIntOrNull() != null && (backend == "dir" || (size.toIntOrNull() ?: 0) > 0)
    val canNext = when (step) {
        0 -> distro.isNotEmpty() && release.isNotEmpty()
        1 -> nameOk && location.isNotEmpty() && numbersOk
        2 -> validCidr(net.address)
        else -> true
    }
    AreaScreen(stringResource(R.string.ct_new), onBack, actions = {
        if (step == 0) IconButton(onClick = { refresh++ }) { Icon(Icons.Default.Refresh, stringResource(R.string.ct_refresh)) }
    }) { pad ->
        Column(Modifier.padding(pad)) {
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                steps.forEachIndexed { i, s -> FilterChip(step == i, { if (i < step || canNext) step = i }, { Text("${i + 1}. " + stringResource(s)) }) }
            }
            Box(Modifier.weight(1f)) {
                when (step) {
                    0 -> Refreshable(images, androidx.compose.foundation.layout.PaddingValues()) { t ->
                        ScrollColumn {
                            Field(stringResource(R.string.ct_search), search, { search = it })
                            SwitchLine(stringResource(R.string.ct_all_images), all) { all = it }
                            filterImages(t.images, search, all).forEach { img ->
                                val sel = img.distro == distro && img.release == release
                                Card(Modifier.fillMaxWidth().clickable { distro = img.distro; release = img.release },
                                    border = if (sel) BorderStroke(2.dp, MaterialTheme.colorScheme.primary) else null,
                                    colors = if (sel) CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.primaryContainer) else CardDefaults.cardColors()) {
                                    Column(Modifier.padding(12.dp)) {
                                        Text(img.label.ifEmpty { "${img.distro} ${img.release}" }, style = MaterialTheme.typography.bodyLarge)
                                        Text("${img.distro} ${img.release} · ${img.arch} · ${img.build}", style = MaterialTheme.typography.bodySmall)
                                    }
                                }
                            }
                            Text(stringResource(R.string.ct_images_help, t.server, formatDate(t.updated)), style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                    1 -> ScrollColumn {
                        SectionCard {
                            Field(stringResource(R.string.cam_name), name, { name = it.lowercase().trim() }, help = stringResource(R.string.ct_name_help),
                                error = if (name.isNotEmpty() && !nameOk) stringResource(R.string.ct_name_rule) else null)
                            Field(stringResource(R.string.vm_description), desc, { desc = it })
                        }
                        SectionCard(stringResource(R.string.ct_storage)) {
                            val l = locs.value.orEmpty()
                            val cur = l.firstOrNull { it.path == location }
                            if (l.isEmpty() && locs.value != null) Text(stringResource(R.string.ct_no_volume), color = MaterialTheme.colorScheme.error)
                            Choice(stringResource(R.string.ct_volume), l.map { it.path to "${it.label} (${it.fsType}, ${formatBytes(it.free)})" }, location, { p ->
                                location = p
                                backend = l.firstOrNull { it.path == p }?.backends?.firstOrNull() ?: "image"
                            })
                            Choice(stringResource(R.string.ct_backend), (cur?.backends ?: listOf("image", "dir")).map { it to backendLabel(it) }, backend, { backend = it })
                            Text(stringResource(when (backend) {
                                "btrfs" -> R.string.ct_backend_btrfs_help
                                "zfs" -> R.string.ct_backend_zfs_help
                                "dir" -> R.string.ct_backend_dir_help
                                else -> R.string.ct_backend_image_help
                            }), style = MaterialTheme.typography.bodySmall)
                            if (backend != "dir") Field(stringResource(R.string.ct_disk), size, { size = it.filter(Char::isDigit) }, number = true)
                        }
                        SectionCard(stringResource(R.string.ct_limits)) {
                            Field(stringResource(R.string.ct_cpus), cpus, { cpus = it.filter { c -> c.isDigit() || c == '.' } }, number = true, help = stringResource(R.string.ct_no_limit))
                            Field(stringResource(R.string.ct_memory), mem, { mem = it.filter(Char::isDigit) }, number = true, help = stringResource(R.string.ct_no_limit))
                            Field(stringResource(R.string.ct_swap), swap, { swap = it.filter(Char::isDigit) }, number = true, enabled = (mem.toIntOrNull() ?: 0) > 0)
                        }
                    }
                    2 -> ScrollColumn { SectionCard(stringResource(R.string.ct_network)) { NetworkEditor(net, bridges) { net = it } } }
                    else -> ScrollColumn {
                        SectionCard(stringResource(R.string.ct_mounts)) { MountsEditor(mounts, privileged, emptyList()) { mounts = it } }
                        SectionCard(stringResource(R.string.ct_options)) {
                            FeaturesEditor(features, privileged) { features = it }
                            Field(stringResource(R.string.ct_root_password), password, { password = it }, secret = true, help = stringResource(R.string.ct_root_password_help))
                            SwitchLine(stringResource(R.string.ct_start_now), start) { start = it }
                            SwitchLine(stringResource(R.string.ct_autostart), autostart) { autostart = it }
                            SwitchLine(stringResource(R.string.ct_privileged), privileged) { privileged = it }
                            if (privileged) Text(stringResource(R.string.ct_privileged_warning), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                        }
                        Text(stringResource(R.string.ct_summary, "$distro $release", if (backend == "dir") backendLabel("dir") else "$size GB", backendLabel(backend), location),
                            style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
            Row(Modifier.fillMaxWidth().padding(12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (step > 0) OutlinedButton(onClick = { step-- }) { Text(stringResource(R.string.b_back)) }
                Box(Modifier.weight(1f))
                if (step < steps.size - 1) Button(enabled = canNext, onClick = { step++ }) { Text(stringResource(R.string.ct_next)) }
                else Button(enabled = nameOk && location.isNotEmpty() && numbersOk && validCidr(net.address) && !actions.busy, onClick = {
                    val req = LxcCreateReq(
                        name = name, description = desc, image = LxcImageRef(distro, release), privileged = privileged, location = location, backend = backend,
                        sizeGb = if (backend == "dir") 0 else size.toInt(), cpus = cpus.toDouble(), memoryMb = mem.toInt(), swapMb = swap.toInt(),
                        network = net, mounts = mounts, features = features, autostart = autostart, start = start, rootPassword = password,
                    )
                    actions.run { job = it.lxcCreate(req) }
                }) { Text(stringResource(if (start) R.string.ct_create_start else R.string.vm_create_btn)) }
            }
        }
    }
    job?.let { j -> JobDialog(j) { ok -> job = null; if (ok != false) onBack() } }
}
