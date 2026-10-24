package io.github.nathplay15.strataos.ui.storage

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Album
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.Layers
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.CreatePool
import io.github.nathplay15.strataos.net.CreateRaid
import io.github.nathplay15.strataos.net.CreateVolume
import io.github.nathplay15.strataos.net.Disk
import io.github.nathplay15.strataos.net.RaidArray
import io.github.nathplay15.strataos.net.VDev
import io.github.nathplay15.strataos.net.VolumeView
import io.github.nathplay15.strataos.net.ZPool
import io.github.nathplay15.strataos.net.btrfsAction
import io.github.nathplay15.strataos.net.createPool
import io.github.nathplay15.strataos.net.createRaid
import io.github.nathplay15.strataos.net.createVolume
import io.github.nathplay15.strataos.net.deleteRaid
import io.github.nathplay15.strataos.net.deleteVolume
import io.github.nathplay15.strataos.net.destroyPool
import io.github.nathplay15.strataos.net.disks
import io.github.nathplay15.strataos.net.enableVolumeQuota
import io.github.nathplay15.strataos.net.poolAction
import io.github.nathplay15.strataos.net.raidAction
import io.github.nathplay15.strataos.net.raids
import io.github.nathplay15.strataos.net.smart
import io.github.nathplay15.strataos.net.smartTest
import io.github.nathplay15.strataos.net.volumes
import io.github.nathplay15.strataos.net.zfs
import io.github.nathplay15.strataos.ui.Chip
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.InfoRow
import io.github.nathplay15.strataos.ui.Loaded
import io.github.nathplay15.strataos.ui.RefreshAction
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.SectionScaffold
import io.github.nathplay15.strataos.ui.Tone
import io.github.nathplay15.strataos.ui.UsageBar
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.color
import io.github.nathplay15.strataos.ui.errorText
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import io.github.nathplay15.strataos.ui.toast
import io.github.nathplay15.strataos.ui.toneOf

/** Everything the storage tabs show, loaded together (one pull to refresh). */
private class StorageData(val volumes: List<VolumeView>, val disks: List<Disk>, val raids: List<RaidArray>, val zfsAvailable: Boolean, val pools: List<ZPool>)

/** Storage manager (administrators): volumes, disks, RAID, ZFS, snapshots, shared folders, quotas. */
@Composable
fun StorageSection(back: () -> Unit) {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    val load = rememberLoad(Unit) {
        val a = api()
        val z = runCatching { a.zfs() }.getOrNull()
        StorageData(a.volumes(), a.disks(), runCatching { a.raids() }.getOrDefault(emptyList()), z?.available == true, z?.pools.orEmpty())
    }
    val tabs = listOf(R.string.a_volumes, R.string.a_disks, R.string.a_raid, R.string.a_zfs, R.string.a_snapshots, R.string.a_shared_folders, R.string.a_quotas).map { stringResource(it) }
    var creating by remember { mutableStateOf<CreateKind?>(null) }
    val act = rememberActions()
    SectionScaffold(
        stringResource(R.string.a_storage), back, actions = { RefreshAction(load.reload) }, tabs = tabs, tab = tab, onTab = { tab = it },
        fab = {
            val kind = when (tab) { 0 -> CreateKind.VOLUME; 2 -> CreateKind.RAID; 3 -> if (load.value?.zfsAvailable == true) CreateKind.POOL else null; else -> null }
            if (kind != null && load.value != null) ExtendedFloatingActionButton(onClick = { creating = kind }, icon = { Icon(Icons.Default.Add, null) },
                text = { Text(stringResource(when (kind) { CreateKind.VOLUME -> R.string.a_new_volume; CreateKind.RAID -> R.string.a_new_raid; CreateKind.POOL -> R.string.a_new_pool })) })
        },
    ) { m ->
        Box(m) {
            when (tab) {
                4 -> SnapshotsTab()
                5 -> SharesTab()
                6 -> QuotasTab()
                else -> Refreshable(load) { d ->
                    when (tab) {
                        0 -> VolumesTab(d.volumes, load.reload)
                        1 -> DisksTab(d.disks)
                        2 -> RaidTab(d.raids, load.reload)
                        else -> ZfsTab(d.zfsAvailable, d.pools, load.reload)
                    }
                }
            }
        }
    }
    val d = load.value
    val kind = creating
    if (kind != null && d != null) {
        val cands = if (kind == CreateKind.VOLUME) volumeCandidates(d.disks, d.raids) else freeDisks(d.disks)
        CreateFlow(kind, cands, onDismiss = { creating = null }) { c ->
            creating = null
            act.job(load.reload) {
                when (kind) {
                    CreateKind.VOLUME -> api().createVolume(CreateVolume(c.name, c.fsType, c.devices, dataProfile = if (c.fsType == "btrfs") c.layout else "", compression = c.compression))
                    CreateKind.RAID -> api().createRaid(CreateRaid(c.name, c.layout, c.devices))
                    CreateKind.POOL -> api().createPool(CreatePool(c.name, listOf(VDev(c.layout, c.devices)), compression = if (c.compression) "lz4" else "off"))
                }
            }
        }
    }
}

@Composable
internal fun ItemCard(title: String, chip: String, sub: String, menu: (@Composable (close: () -> Unit) -> Unit)? = null, onClick: (() -> Unit)? = null, body: @Composable () -> Unit = {}) {
    var open by remember { mutableStateOf(false) }
    Card(Modifier.fillMaxWidth().let { if (onClick != null) it.clickable(onClick = onClick) else it }) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(title, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                if (chip.isNotEmpty()) Chip(chip, toneOf(chip))
                if (menu != null) Box {
                    IconButton(onClick = { open = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.a_actions_for, title)) }
                    DropdownMenu(open, { open = false }) { menu { open = false } }
                }
            }
            if (sub.isNotEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            body()
        }
    }
}

@Composable
internal fun CardList(content: LazyListScope.() -> Unit) =
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(12.dp, 12.dp, 12.dp, 88.dp), verticalArrangement = Arrangement.spacedBy(10.dp), content = content)

@Composable
private fun VolumesTab(vols: List<VolumeView>, reload: () -> Unit) {
    val act = rememberActions()
    var deleting by remember { mutableStateOf<VolumeView?>(null) }
    if (vols.isEmpty()) return EmptyState(Icons.Default.Storage, stringResource(R.string.a_no_volumes))
    CardList {
        items(vols, key = { it.id }) { v ->
            ItemCard(v.name, v.health, listOf(v.fsType + (if (v.profile.isNotEmpty()) " " + v.profile else ""), v.mountPoint).joinToString(" · "),
                menu = { close ->
                    if (v.fsType == "btrfs") DropdownMenuItem({ Text(stringResource(R.string.a_scrub)) }, { close(); act.run(reload) { api().btrfsAction(v.id, "scrub") } })
                    if (v.fsType == "ext4" || v.fsType == "xfs") DropdownMenuItem({ Text(stringResource(R.string.a_enable_quotas)) }, { close(); act.job(reload) { api().enableVolumeQuota(v.id) } })
                    DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, { close(); deleting = v })
                }) {
                if (v.mounted) UsageBar(v.usage.used, v.usage.total)
                if (!v.shares.isNullOrEmpty()) Text(stringResource(R.string.a_used_by_shares, v.shares.joinToString(", ")), style = MaterialTheme.typography.bodySmall)
                if (!v.devices.isNullOrEmpty()) Text(v.devices.joinToString(", "), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
            }
        }
    }
    deleting?.let { v ->
        var wipe by remember { mutableStateOf(false) }
        ConfirmDialog(stringResource(R.string.a_delete_volume, v.name), stringResource(R.string.a_delete_volume_text), stringResource(R.string.a_delete), danger = true,
            typeToConfirm = v.name, details = v.devices.orEmpty(), onDismiss = { deleting = null },
            onConfirm = { act.run(reload) { api().deleteVolume(v.id, wipe) } }) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(stringResource(R.string.a_wipe_disks), Modifier.weight(1f), style = MaterialTheme.typography.bodySmall)
                Switch(wipe, { wipe = it })
            }
        }
    }
}

@Composable
private fun DisksTab(disks: List<Disk>) {
    var smartOf by remember { mutableStateOf<Disk?>(null) }
    if (disks.isEmpty()) return EmptyState(Icons.Default.Album, stringResource(R.string.a_no_disks))
    CardList {
        items(disks, key = { it.path }) { d ->
            val health = when (d.healthy) { true -> "healthy"; false -> "failed"; null -> if (d.standby) "standby" else "" }
            ItemCard(d.title, health, listOf(formatBytes(d.size), d.kind.uppercase(), d.path, d.serial).filter { it.isNotBlank() }.joinToString(" · "), onClick = { smartOf = d }) {
                Text(buildList {
                    add(when {
                        d.system -> stringResource(R.string.a_disk_system)
                        d.usage == "free" -> stringResource(R.string.a_disk_free)
                        d.usedBy.isNotEmpty() -> stringResource(R.string.a_disk_used_by, d.usage, d.usedBy)
                        else -> d.usage
                    })
                    if (d.temperature > 0) add("${d.temperature} °C")
                    if (d.powerOnHours > 0) add(stringResource(R.string.a_power_on, d.powerOnHours))
                }.filter { it.isNotBlank() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
    smartOf?.let { SmartDialog(it) { smartOf = null } }
}

@Composable
private fun SmartDialog(d: Disk, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    val res = androidx.compose.ui.platform.LocalResources.current
    val load = rememberLoad(d.path) { api().smart(d.path) }
    val act = rememberActions()
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(d.title) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                when (val s = load.state) {
                    Loaded.Loading -> LinearProgressIndicator(Modifier.fillMaxWidth())
                    is Loaded.Failed -> Text(errorText(ctx, s.error), color = MaterialTheme.colorScheme.error)
                    is Loaded.Ok -> {
                        val i = s.value
                        if (!i.available) Text(stringResource(R.string.a_smart_unavailable))
                        InfoRow(stringResource(R.string.a_smart_result), when (i.passed) { true -> stringResource(R.string.a_smart_passed); false -> stringResource(R.string.a_smart_failed); null -> "" })
                        InfoRow(stringResource(R.string.a_serial), i.serial)
                        InfoRow(stringResource(R.string.a_firmware), i.firmware)
                        if (i.temperature > 0) InfoRow(stringResource(R.string.a_temperature), "${i.temperature} °C")
                        if (i.powerOnHours > 0) InfoRow(stringResource(R.string.a_power_on_label), stringResource(R.string.a_power_on, i.powerOnHours))
                        if (i.powerCycles > 0) InfoRow(stringResource(R.string.a_power_cycles), i.powerCycles.toString())
                        i.warnings.orEmpty().forEach { Text("⚠ $it", color = Tone.WARN.color()) }
                        val attrs = i.attributes.orEmpty()
                        if (attrs.isNotEmpty()) Text(stringResource(R.string.a_smart_attributes), style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(top = 8.dp))
                        attrs.forEach { a ->
                            Text("${a.id} ${a.name}: ${a.raw}" + if (a.failed.isNotEmpty()) " (${a.failed})" else "", style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                                color = if (a.failed.isNotEmpty()) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurface)
                        }
                        if (i.available) Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(onClick = { act.run({ toast(ctx, res.getString(R.string.a_smart_test_started)) }) { api().smartTest(d.path, "short") } }) { Text(stringResource(R.string.a_smart_short)) }
                            OutlinedButton(onClick = { act.run({ toast(ctx, res.getString(R.string.a_smart_test_started)) }) { api().smartTest(d.path, "long") } }) { Text(stringResource(R.string.a_smart_long)) }
                        }
                    }
                }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.a_close)) } },
    )
}

@Composable
private fun RaidTab(raids: List<RaidArray>, reload: () -> Unit) {
    val act = rememberActions()
    var deleting by remember { mutableStateOf<RaidArray?>(null) }
    if (raids.isEmpty()) return EmptyState(Icons.Default.Layers, stringResource(R.string.a_no_raid))
    CardList {
        items(raids, key = { it.name }) { a ->
            ItemCard(a.label.ifEmpty { a.name }, a.health, listOf("RAID " + a.level.removePrefix("raid"), formatBytes(a.size), a.device).joinToString(" · "),
                menu = { close ->
                    if (a.syncAction == "check") DropdownMenuItem({ Text(stringResource(R.string.a_stop_check)) }, { close(); act.run(reload) { api().raidAction(a.name, "idle") } })
                    else DropdownMenuItem({ Text(stringResource(R.string.a_check_raid)) }, { close(); act.run(reload) { api().raidAction(a.name, "check") } })
                    DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, { close(); deleting = a })
                }) {
                Text(stringResource(R.string.a_raid_members, a.activeDevices, a.raidDevices, a.failedDevices, a.spareDevices), style = MaterialTheme.typography.bodySmall)
                a.members.orEmpty().forEach { m -> Text("${m.device} — ${m.state}", style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace) }
                if (a.syncAction.isNotEmpty()) {
                    LinearProgressIndicator(progress = { (a.syncProgress / 100).toFloat() }, modifier = Modifier.fillMaxWidth().padding(top = 4.dp))
                    Text(stringResource(R.string.a_raid_sync, a.syncAction, a.syncProgress.toInt(), a.syncFinish), style = MaterialTheme.typography.bodySmall)
                }
                if (a.usedBy.isNotEmpty()) Text(stringResource(R.string.a_used_by, a.usedBy), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
    deleting?.let { a ->
        ConfirmDialog(stringResource(R.string.a_delete_raid, a.label.ifEmpty { a.name }), stringResource(R.string.a_delete_raid_text), stringResource(R.string.a_delete), danger = true,
            typeToConfirm = a.label.ifEmpty { a.name }, details = a.members.orEmpty().map { it.device }, onDismiss = { deleting = null },
            onConfirm = { act.run(reload) { api().deleteRaid(a.name) } })
    }
}

@Composable
private fun ZfsTab(available: Boolean, pools: List<ZPool>, reload: () -> Unit) {
    val act = rememberActions()
    var destroying by remember { mutableStateOf<ZPool?>(null) }
    if (!available) return EmptyState(Icons.Default.Dns, stringResource(R.string.a_zfs_missing))
    if (pools.isEmpty()) return EmptyState(Icons.Default.Dns, stringResource(R.string.a_no_pools))
    CardList {
        items(pools, key = { it.name }) { p ->
            ItemCard(p.name, p.health.lowercase(), listOf(formatBytes(p.size), stringResource(R.string.a_fragmentation, p.fragmentation)).joinToString(" · "),
                menu = { close ->
                    DropdownMenuItem({ Text(stringResource(R.string.a_scrub)) }, { close(); act.run(reload) { api().poolAction(p.name, "scrub") } })
                    DropdownMenuItem({ Text(stringResource(R.string.a_stop_scrub)) }, { close(); act.run(reload) { api().poolAction(p.name, "scrub-stop") } })
                    DropdownMenuItem({ Text(stringResource(R.string.a_destroy), color = MaterialTheme.colorScheme.error) }, { close(); destroying = p })
                }) {
                UsageBar(p.alloc, p.size)
                p.problems.orEmpty().forEach { pr -> Text("⚠ ${pr.was.ifEmpty { pr.device }}: ${pr.state}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error) }
                p.datasets.orEmpty().filter { it.type == "filesystem" }.forEach { ds ->
                    Text("${ds.name} — ${formatBytes(ds.used)} · ${ds.mountpoint}", style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                }
            }
        }
    }
    destroying?.let { p ->
        ConfirmDialog(stringResource(R.string.a_destroy_pool, p.name), stringResource(R.string.a_destroy_pool_text), stringResource(R.string.a_destroy), danger = true,
            typeToConfirm = p.name, onDismiss = { destroying = null }, onConfirm = { act.run(reload) { api().destroyPool(p.name) } })
    }
}
