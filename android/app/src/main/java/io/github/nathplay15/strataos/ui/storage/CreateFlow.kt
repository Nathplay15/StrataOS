package io.github.nathplay15.strataos.ui.storage

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.formatBytes

enum class CreateKind { VOLUME, RAID, POOL }

/** What the flow produced; [layout] is the RAID level, ZFS vdev type or Btrfs profile. */
data class CreateChoice(val name: String, val fsType: String, val layout: String, val devices: List<String>, val compression: Boolean)

/**
 * Creation of a volume, RAID array or ZFS pool, made for a phone: one screen
 * to choose, then a review that lists every device to be erased and asks to
 * type the name before anything happens.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CreateFlow(kind: CreateKind, candidates: List<Candidate>, onDismiss: () -> Unit, onCreate: (CreateChoice) -> Unit) {
    var name by remember { mutableStateOf(when (kind) { CreateKind.VOLUME -> "volume1"; CreateKind.RAID -> "md0"; CreateKind.POOL -> "tank" }) }
    var fs by remember { mutableStateOf("ext4") }
    val layouts = when (kind) { CreateKind.RAID -> raidLevels; CreateKind.POOL -> zfsLayouts; CreateKind.VOLUME -> btrfsProfiles }
    var layout by remember { mutableStateOf(when (kind) { CreateKind.RAID -> "1"; CreateKind.POOL -> "mirror"; CreateKind.VOLUME -> "raid1" }) }
    var picked by remember { mutableStateOf(listOf<String>()) }
    var compression by remember { mutableStateOf(true) }
    var review by remember { mutableStateOf(false) }
    val multi = kind != CreateKind.VOLUME || fs == "btrfs"
    val chosen = candidates.filter { it.path in picked }
    val lay = layouts.first { it.id == layout }
    val effective = if (kind == CreateKind.VOLUME && (fs != "btrfs" || chosen.size < 2)) null else lay
    val enough = chosen.isNotEmpty() && (effective == null || chosen.size >= effective.min)
    val smallest = chosen.minOfOrNull { it.size } ?: 0
    val capacity = if (effective != null) capacityOf(effective, chosen.size, smallest) else smallest
    val title = stringResource(when (kind) { CreateKind.VOLUME -> R.string.a_new_volume; CreateKind.RAID -> R.string.a_new_raid; CreateKind.POOL -> R.string.a_new_pool })

    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Scaffold(
            contentWindowInsets = WindowInsets(0, 0, 0, 0),
            topBar = { TopAppBar(title = { Text(title) }, navigationIcon = { IconButton(onClick = onDismiss) { Icon(Icons.Default.Close, stringResource(R.string.cancel)) } }) },
        ) { pad ->
            Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                OutlinedTextField(name, { name = it.trim() }, label = { Text(stringResource(R.string.name)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
                    isError = !validStorageName(name), supportingText = { Text(stringResource(R.string.a_name_rule)) })
                if (kind == CreateKind.VOLUME) {
                    Text(stringResource(R.string.a_filesystem), style = MaterialTheme.typography.titleSmall)
                    listOf("ext4" to R.string.a_fs_ext4, "xfs" to R.string.a_fs_xfs, "btrfs" to R.string.a_fs_btrfs).forEach { (id, label) ->
                        Radio(stringResource(label), fs == id) { fs = id; if (id != "btrfs" && picked.size > 1) picked = picked.take(1) }
                    }
                }
                if (kind != CreateKind.VOLUME || (fs == "btrfs" && chosen.size > 1)) {
                    Text(stringResource(if (kind == CreateKind.VOLUME) R.string.a_btrfs_profile else R.string.a_layout), style = MaterialTheme.typography.titleSmall)
                    layouts.forEach { l ->
                        Radio("${l.label} — " + stringResource(R.string.a_min_devices, l.min) + if (!l.redundant) " · " + stringResource(R.string.a_no_redundancy) else "", layout == l.id) { layout = l.id }
                    }
                }
                Text(stringResource(if (multi) R.string.a_pick_devices else R.string.a_pick_device), style = MaterialTheme.typography.titleSmall)
                if (candidates.isEmpty()) Text(stringResource(R.string.a_no_free_disks), color = MaterialTheme.colorScheme.onSurfaceVariant)
                candidates.forEach { c ->
                    val on = c.path in picked
                    Row(Modifier.fillMaxWidth().toggleable(on) { v -> picked = if (!multi) (if (v) listOf(c.path) else emptyList()) else if (v) picked + c.path else picked - c.path }.padding(vertical = 4.dp),
                        verticalAlignment = Alignment.CenterVertically) {
                        if (multi) Checkbox(on, null) else RadioButton(on, null)
                        Column(Modifier.padding(start = 8.dp)) {
                            Text("${c.title} — ${formatBytes(c.size)}")
                            Text(listOf(c.path, c.detail).filter { it.isNotBlank() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
                if (kind == CreateKind.POOL || (kind == CreateKind.VOLUME && fs == "btrfs")) Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.a_compression), Modifier.weight(1f))
                    Switch(compression, { compression = it })
                }
                if (chosen.isNotEmpty()) Text(stringResource(R.string.a_capacity, formatBytes(capacity)), style = MaterialTheme.typography.bodyMedium)
                if (effective != null && !effective.redundant && chosen.size > 1 || kind != CreateKind.VOLUME && !lay.redundant)
                    Warning(stringResource(R.string.a_no_redundancy_warning))
                if (effective != null && chosen.isNotEmpty() && chosen.size < effective.min) Warning(stringResource(R.string.a_need_devices, effective.min))
                Button(onClick = { review = true }, enabled = enough && validStorageName(name), modifier = Modifier.fillMaxWidth(),
                    colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error)) { Text(stringResource(R.string.a_review)) }
            }
        }
        if (review) ConfirmDialog(
            title = stringResource(R.string.a_erase_title, chosen.size),
            text = stringResource(R.string.a_erase_text),
            details = eraseList(chosen) { formatBytes(it) },
            confirm = stringResource(R.string.a_erase_create), danger = true, typeToConfirm = name,
            onDismiss = { review = false },
            onConfirm = {
                onCreate(CreateChoice(name, if (kind == CreateKind.VOLUME) fs else "", if (effective != null) layout else if (kind == CreateKind.VOLUME && fs == "btrfs") "single" else "", chosen.map { it.path }, compression))
            },
        )
    }
}

@Composable
private fun Radio(label: String, selected: Boolean, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().selectable(selected, onClick = onClick).padding(vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        RadioButton(selected, null)
        Text(label, Modifier.padding(start = 8.dp))
    }
}

@Composable
fun Warning(text: String) {
    Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.errorContainer)) {
        Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Default.Warning, null, tint = MaterialTheme.colorScheme.onErrorContainer)
            Text(text, Modifier.padding(start = 8.dp), color = MaterialTheme.colorScheme.onErrorContainer, style = MaterialTheme.typography.bodySmall)
        }
    }
}
