package io.github.nathplay15.strataos.ui.vms

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
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
import io.github.nathplay15.strataos.net.CloudInit
import io.github.nathplay15.strataos.net.NasJob
import io.github.nathplay15.strataos.net.VmCloudReq
import io.github.nathplay15.strataos.net.VmCreateReq
import io.github.nathplay15.strataos.net.VmList
import io.github.nathplay15.strataos.net.adminSshKeys
import io.github.nathplay15.strataos.net.cloudImages
import io.github.nathplay15.strataos.net.createCloudVm
import io.github.nathplay15.strataos.net.createVm
import io.github.nathplay15.strataos.net.storageVolumes
import io.github.nathplay15.strataos.net.vmIsos
import io.github.nathplay15.strataos.net.vmNameRe
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes

/** What the wizard checks before it lets the NAS create the VM (the NAS checks again). */
fun vmFormProblems(name: String, vcpus: Int, memMb: Int, diskGb: Int, storage: String, cloud: Boolean, iso: String, image: String, user: String): List<String> = buildList {
    if (!vmNameRe.matches(name)) add("name")
    if (vcpus !in 1..64) add("vcpus")
    if (memMb !in 256..1024 * 1024) add("memory")
    if (diskGb !in 1..64 * 1024) add("disk")
    if (!storage.startsWith("/srv/strata/")) add("storage")
    if (!cloud && iso.isEmpty()) add("iso")
    if (cloud && image.isEmpty()) add("image")
    if (cloud && !Regex("^[a-z_][a-z0-9_-]{0,31}$").matches(user)) add("user")
}

/**
 * Creates a VM in three steps: what it boots (an ISO to install from, or a
 * ready cloud image set up by cloud-init), its resources and network, then a
 * summary. The NAS creates it in the background (a job).
 */
@Composable
fun VmCreate(list: VmList?, onBack: () -> Unit) {
    var step by rememberSaveable { mutableIntStateOf(0) }
    var cloud by rememberSaveable { mutableStateOf(false) }
    var iso by rememberSaveable { mutableStateOf("") }
    var image by rememberSaveable { mutableStateOf("") }
    var name by rememberSaveable { mutableStateOf("") }
    var os by rememberSaveable { mutableStateOf("linux") }
    var cpus by rememberSaveable { mutableStateOf("2") }
    var mem by rememberSaveable { mutableStateOf("2048") }
    var disk by rememberSaveable { mutableStateOf("32") }
    var storage by rememberSaveable { mutableStateOf("") }
    var network by rememberSaveable { mutableStateOf("nat") }
    var firmware by rememberSaveable { mutableStateOf("uefi") }
    var autostart by rememberSaveable { mutableStateOf(false) }
    var user by rememberSaveable { mutableStateOf("admin") }
    var pass by rememberSaveable { mutableStateOf("") }
    var keys by rememberSaveable { mutableStateOf("") }
    var job by remember { mutableStateOf<NasJob?>(null) }
    val actions = rememberActions()
    val isos = rememberData(Unit) { it.vmIsos() }
    val images = rememberData(Unit) { it.cloudImages() }
    val vols = rememberData(Unit) { it.storageVolumes().filter { v -> v.mounted } }
    val sshKeys = rememberData(Unit) { it.adminSshKeys() }
    LaunchedEffect(vols.value) { if (storage.isEmpty()) vols.value?.firstOrNull()?.let { storage = it.mountPoint.trimEnd('/') + "/vms" } }
    LaunchedEffect(sshKeys.value) { if (keys.isEmpty()) keys = sshKeys.value.orEmpty().joinToString("\n") }

    val problems = vmFormProblems(name.trim(), cpus.toIntOrNull() ?: 0, mem.toIntOrNull() ?: 0, disk.toIntOrNull() ?: 0, storage.trim(), cloud, iso, image, user)
    val back: () -> Unit = { if (step > 0) step-- else onBack() }
    AreaScreen(stringResource(R.string.vm_create), onBack = back, subtitle = stringResource(R.string.vm_step, step + 1, 3)) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                when (step) {
                    0 -> SectionCard(stringResource(R.string.vm_source)) {
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            FilterChip(selected = !cloud, onClick = { cloud = false }, label = { Text(stringResource(R.string.vm_from_iso)) })
                            FilterChip(selected = cloud, onClick = { cloud = true; os = "linux" }, label = { Text(stringResource(R.string.vm_from_cloud)) })
                        }
                        if (!cloud) {
                            val l = isos.value.orEmpty()
                            if (l.isEmpty()) Text(stringResource(R.string.vm_no_iso), style = MaterialTheme.typography.bodySmall)
                            else Choice(stringResource(R.string.vm_iso), listOf("" to "—") + l.map { it to it.substringAfterLast('/') }, iso, { iso = it })
                            Choice(stringResource(R.string.vm_os), listOf("linux" to "Linux", "windows" to "Windows", "other" to stringResource(R.string.vm_os_other)), os, { os = it })
                        } else {
                            val l = images.value.orEmpty()
                            Choice(stringResource(R.string.vm_image), listOf("" to "—") + l.map { it.id to it.name + if (it.cached) " ✓" else "" }, image, { image = it })
                            Text(stringResource(R.string.vm_image_help), style = MaterialTheme.typography.bodySmall)
                        }
                    }
                    1 -> {
                        SectionCard(stringResource(R.string.vm_resources_title)) {
                            Field(stringResource(R.string.cam_name), name, { name = it }, error = if (name.isNotEmpty() && "name" in problems) stringResource(R.string.vm_name_rule) else null)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Field(stringResource(R.string.vm_vcpus), cpus, { cpus = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true, error = if ("vcpus" in problems) "1–64" else null)
                                Field(stringResource(R.string.vm_memory_mb), mem, { mem = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true, error = if ("memory" in problems) "≥ 256" else null)
                            }
                            Field(stringResource(R.string.vm_disk_gb), disk, { disk = it.filter(Char::isDigit) }, number = true, error = if ("disk" in problems) "≥ 1" else null)
                            val v = vols.value.orEmpty()
                            if (v.isNotEmpty()) Choice(stringResource(R.string.cam_volume), v.map { it.mountPoint.trimEnd('/') + "/vms" to "${it.name} (${stringResource(R.string.cam_free, formatBytes(it.usage.free))})" }
                                .let { o -> if (o.none { it.first == storage }) o + (storage to storage) else o }, storage, { storage = it })
                            Field(stringResource(R.string.vm_storage), storage, { storage = it }, error = if ("storage" in problems) stringResource(R.string.vm_storage_rule) else null)
                            Choice(stringResource(R.string.vm_network), listOf("nat" to stringResource(R.string.vm_nat)) + list?.bridges.orEmpty().map { it to stringResource(R.string.vm_bridge, it) }, network, { network = it })
                            Choice(stringResource(R.string.vm_firmware), listOf("uefi" to "UEFI", "bios" to "BIOS"), firmware, { firmware = it })
                            SwitchLine(stringResource(R.string.vm_autostart), autostart) { autostart = it }
                        }
                        if (cloud) SectionCard(stringResource(R.string.vm_cloud_init)) {
                            Field(stringResource(R.string.vm_guest_user), user, { user = it.lowercase() }, error = if ("user" in problems) stringResource(R.string.vm_name_rule) else null)
                            Field(stringResource(R.string.cam_password), pass, { pass = it }, secret = true, help = stringResource(R.string.vm_guest_pass_help))
                            Field(stringResource(R.string.vm_ssh_keys), keys, { keys = it }, singleLine = false, help = stringResource(R.string.vm_ssh_keys_help))
                        }
                    }
                    else -> SectionCard(stringResource(R.string.vm_summary)) {
                        KeyValue(stringResource(R.string.cam_name), name)
                        KeyValue(stringResource(R.string.vm_source), if (cloud) images.value?.firstOrNull { it.id == image }?.name ?: image else iso.substringAfterLast('/'))
                        KeyValue(stringResource(R.string.vm_resources_title), stringResource(R.string.vm_resources, cpus.toIntOrNull() ?: 0, formatBytes((mem.toLongOrNull() ?: 0) * 1024 * 1024), "$disk GB"))
                        KeyValue(stringResource(R.string.vm_storage), storage)
                        KeyValue(stringResource(R.string.vm_network), network)
                        if (problems.isNotEmpty()) Text(stringResource(R.string.vm_form_incomplete), color = MaterialTheme.colorScheme.error)
                    }
                }
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (step > 0) OutlinedButton(onClick = { step-- }) { Text(stringResource(R.string.vm_previous)) }
                    if (step < 2) Button(onClick = { step++ }, enabled = if (step == 0) (if (cloud) image.isNotEmpty() else iso.isNotEmpty()) else problems.isEmpty()) { Text(stringResource(R.string.vm_next)) }
                    else Button(enabled = problems.isEmpty() && !actions.busy, onClick = {
                        actions.run {
                            val c = cpus.toInt(); val m = mem.toInt(); val d = disk.toInt()
                            job = if (cloud) it.createCloudVm(VmCloudReq(name.trim(), "linux", c, m, d, storage.trim(), network, firmware, autostart, image,
                                CloudInit(user = user, password = pass, sshKeys = keys.lines().map(String::trim).filter(String::isNotEmpty))))
                            else it.createVm(VmCreateReq(name.trim(), os, c, m, d, iso, storage.trim(), network, firmware, autostart))
                        }
                    }) { Text(stringResource(R.string.vm_create_btn)) }
                }
            }
        }
    }
    job?.let { j -> JobDialog(j) { ok -> job = null; if (ok != false) onBack() } }
}
