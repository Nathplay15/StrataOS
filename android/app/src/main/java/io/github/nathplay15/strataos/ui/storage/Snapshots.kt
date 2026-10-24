package io.github.nathplay15.strataos.ui.storage

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.BtrfsSubvol
import io.github.nathplay15.strataos.net.VolumeView
import io.github.nathplay15.strataos.net.ZDataset
import io.github.nathplay15.strataos.net.btrfs
import io.github.nathplay15.strataos.net.btrfsAction
import io.github.nathplay15.strataos.net.volumes
import io.github.nathplay15.strataos.net.zfs
import io.github.nathplay15.strataos.net.zfsDestroy
import io.github.nathplay15.strataos.net.zfsRollback
import io.github.nathplay15.strataos.net.zfsSnapshot
import io.github.nathplay15.strataos.net.zfsSnapshots
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.LocalOpenInFiles
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatTime
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

private class Snaps(val zfs: List<ZDataset>, val datasets: List<ZDataset>, val btrfs: List<Pair<VolumeView, List<BtrfsSubvol>>>)

/** A snapshot target: a ZFS dataset or a Btrfs volume. */
private data class SnapTarget(val label: String, val zfs: String = "", val volume: String = "")

/** Default name of a manual snapshot (the NAS names its scheduled ones auto-…). */
fun manualSnapshotName(now: Date = Date()): String = "manual-" + SimpleDateFormat("yyyyMMdd-HHmm", Locale.ROOT).format(now)

@Composable
fun SnapshotsTab() {
    val act = rememberActions()
    val openInFiles = LocalOpenInFiles.current
    val load = rememberLoad(Unit) {
        val a = api()
        val z = runCatching { a.zfs() }.getOrNull()
        val btrfsVols = a.volumes().filter { it.fsType == "btrfs" && it.mounted }
        Snaps(
            if (z?.available == true) a.zfsSnapshots() else emptyList(),
            z?.pools.orEmpty().flatMap { it.datasets.orEmpty() }.filter { it.type == "filesystem" },
            btrfsVols.map { v -> v to runCatching { a.btrfs(v.id).subvolumes.orEmpty().filter { it.snapshot } }.getOrDefault(emptyList()) },
        )
    }
    var taking by remember { mutableStateOf(false) }
    var rollback by remember { mutableStateOf<ZDataset?>(null) }
    var deleting by remember { mutableStateOf<Pair<String, () -> Unit>?>(null) }
    val now = System.currentTimeMillis() / 1000
    Refreshable(load) { s ->
        val empty = s.zfs.isEmpty() && s.btrfs.all { it.second.isEmpty() }
        val targets = s.datasets.map { SnapTarget("${it.name} (ZFS)", zfs = it.name) } + s.btrfs.map { SnapTarget("${it.first.name} (Btrfs)", volume = it.first.id) }
        CardList {
            item {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(stringResource(R.string.a_snapshots_intro), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Button(onClick = { taking = true }, enabled = targets.isNotEmpty()) { Text(stringResource(R.string.a_take_snapshot)) }
                    if (empty) Text(stringResource(if (targets.isEmpty()) R.string.a_no_snapshot_targets else R.string.a_no_snapshots), Modifier.padding(vertical = 24.dp), color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            items(s.zfs, key = { "z" + it.name }) { snap ->
                val locked = snap.lockedUntil > now
                ItemCard(snap.name.substringAfter('@'), if (locked) "locked" else "", listOf(snap.name.substringBefore('@'), formatBytes(snap.used),
                    if (snap.creation > 0) formatTime(snap.creation * 1000) else "").filter { it.isNotEmpty() }.joinToString(" · "),
                    menu = { close ->
                        DropdownMenuItem({ Text(stringResource(R.string.a_rollback)) }, { close(); rollback = snap })
                        if (!locked) DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, {
                            close(); deleting = snap.name to { act.run(load.reload) { api().zfsDestroy(snap.name) } }
                        })
                    }) {
                    if (locked) Text(stringResource(R.string.a_locked_until, formatTime(snap.lockedUntil * 1000)), style = MaterialTheme.typography.bodySmall)
                }
            }
            s.btrfs.forEach { (v, subs) ->
                items(subs, key = { "b" + v.id + it.path }) { sv ->
                    val locked = sv.lockedUntil > now
                    ItemCard(sv.path.substringAfterLast('/'), if (locked) "locked" else "", "${v.name} (Btrfs) · ${sv.path}",
                        menu = { close ->
                            DropdownMenuItem({ Text(stringResource(R.string.a_browse_files)) }, { close(); openInFiles(v.mountPoint + "/" + sv.path) })
                            if (!locked) DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, {
                                close(); deleting = sv.path to { act.run(load.reload) { api().btrfsAction(v.id, "delete-subvolume", path = sv.path) } }
                            })
                        }) {
                        Text(stringResource(R.string.a_btrfs_restore_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
        }
        if (taking) TakeSnapshotDialog(targets, onDismiss = { taking = false }) { t, name ->
            act.run(load.reload) {
                if (t.zfs.isNotEmpty()) api().zfsSnapshot(t.zfs, name, false) else api().btrfsAction(t.volume, "snapshot", name = name)
            }
        }
    }
    rollback?.let { snap ->
        ConfirmDialog(stringResource(R.string.a_rollback_title, snap.name), stringResource(R.string.a_rollback_text), stringResource(R.string.a_rollback), danger = true,
            typeToConfirm = snap.name.substringAfter('@'), onDismiss = { rollback = null }, onConfirm = { act.run(load.reload) { api().zfsRollback(snap.name) } })
    }
    deleting?.let { (name, run) ->
        ConfirmDialog(stringResource(R.string.a_delete_snapshot), name, stringResource(R.string.a_delete), danger = true, onDismiss = { deleting = null }, onConfirm = run)
    }
}

@Composable
private fun TakeSnapshotDialog(targets: List<SnapTarget>, onDismiss: () -> Unit, onTake: (SnapTarget, String) -> Unit) {
    var target by remember { mutableStateOf(targets.first()) }
    var name by remember { mutableStateOf(manualSnapshotName()) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.a_take_snapshot)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                targets.forEach { t ->
                    Row(Modifier.fillMaxWidth().selectable(target == t) { target = t }.padding(vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                        RadioButton(target == t, null)
                        Text(t.label, Modifier.padding(start = 8.dp))
                    }
                }
                OutlinedTextField(name, { name = it.trim() }, label = { Text(stringResource(R.string.name)) }, singleLine = true, modifier = Modifier.fillMaxWidth().padding(top = 8.dp))
            }
        },
        confirmButton = { TextButton(enabled = Regex("^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$").matches(name), onClick = { onDismiss(); onTake(target, name) }) { Text(stringResource(R.string.create)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } },
    )
}
