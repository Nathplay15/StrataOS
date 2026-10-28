package io.github.nathplay15.strataos.ui.sync

import android.Manifest
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.DocumentsContract
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.selection.selectable
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.FolderShared
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.sync.APP_PREFIX
import io.github.nathplay15.strataos.sync.PairStatus
import io.github.nathplay15.strataos.sync.SyncLive
import io.github.nathplay15.strataos.sync.SyncMode
import io.github.nathplay15.strataos.sync.SyncPair
import io.github.nathplay15.strataos.sync.SyncScheduler
import io.github.nathplay15.strataos.sync.SyncShare
import io.github.nathplay15.strataos.sync.SyncWorker
import io.github.nathplay15.strataos.sync.syncShares
import io.github.nathplay15.strataos.ui.LoadedContent
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.formatTime
import io.github.nathplay15.strataos.ui.rememberLoader
import java.util.UUID
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// Folder sync: the pairs (a NAS folder and a phone folder), their state,
// adding and changing them, conflicts and what is not synchronized.
// Routes: "list", "new", "edit:<id>", "pair:<id>".

/** The pairs and their status, read again every second while shown. */
@Composable
private fun rememberPairs(): Pair<List<SyncPair>, Map<String, PairStatus>> {
    var state by remember { mutableStateOf<Pair<List<SyncPair>, Map<String, PairStatus>>>(emptyList<SyncPair>() to emptyMap()) }
    LaunchedEffect(Unit) {
        while (true) {
            state = withContext(Dispatchers.IO) {
                val db = Graph.syncDb
                val pairs = db.pairs()
                pairs to pairs.associate { it.id to db.status(it.id) }
            }
            delay(1000)
        }
    }
    return state
}

@Composable
fun SyncScreen(onBack: () -> Unit) {
    var route by rememberSaveable { mutableStateOf("list") }
    val (kind, id) = route.split(":", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
    val (pairs, statuses) = rememberPairs()
    val home = { route = "list" }
    when (kind) {
        "new" -> PairEditor(null, onDone = home)
        "edit" -> pairs.firstOrNull { it.id == id }?.let { PairEditor(it, onDone = { route = "pair:$id" }) } ?: PairList(pairs, statuses, onBack) { route = it }
        "pair" -> pairs.firstOrNull { it.id == id }?.let { PairDetail(it, statuses[id] ?: PairStatus(), onBack = home) { route = it } } ?: PairList(pairs, statuses, onBack) { route = it }
        else -> PairList(pairs, statuses, onBack) { route = it }
    }
}

/** The label and tone of a pair's state. */
@Composable
fun stateLabel(p: SyncPair, s: PairStatus): Pair<String, Tone> = when {
    p.paused -> stringResource(R.string.sync_state_paused) to Tone.Neutral
    s.brake != null -> stringResource(R.string.sync_state_brake) to Tone.Warn
    s.state == "syncing" && s.pending > 0 -> pluralStringResource(R.plurals.sync_state_syncing_n, s.pending - s.done, s.pending - s.done) to Tone.Accent
    s.state == "syncing" -> stringResource(R.string.sync_state_syncing) to Tone.Accent
    s.state == "error" -> stringResource(R.string.sync_state_error) to Tone.Bad
    s.lastSync == 0L -> stringResource(R.string.sync_state_waiting) to Tone.Neutral
    else -> stringResource(R.string.sync_state_ok) to Tone.Ok
}

@Composable
private fun modeLabel(mode: String) = stringResource(when (mode) {
    SyncMode.UP -> R.string.sync_mode_up
    SyncMode.DOWN -> R.string.sync_mode_down
    else -> R.string.sync_mode_both
})

@Composable
private fun PairList(pairs: List<SyncPair>, statuses: Map<String, PairStatus>, onBack: () -> Unit, go: (String) -> Unit) {
    val ctx = LocalContext.current
    val askNotif = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { }
    AreaScreen(stringResource(R.string.sync_title), onBack, fab = {
        ExtendedFloatingActionButton(onClick = {
            if (Build.VERSION.SDK_INT >= 33) askNotif.launch(Manifest.permission.POST_NOTIFICATIONS)
            go("new")
        }, icon = { Icon(Icons.Default.Add, null) }, text = { Text(stringResource(R.string.sync_add)) })
    }, actions = {
        if (pairs.isNotEmpty()) IconButton(onClick = { SyncScheduler.runAll(ctx, manual = true) }) { Icon(Icons.Default.Sync, stringResource(R.string.sync_now)) }
    }) { pad ->
        if (pairs.isEmpty()) {
            Box(Modifier.padding(pad)) { Empty(Icons.Default.Sync, stringResource(R.string.sync_none)) }
            return@AreaScreen
        }
        LazyColumn(Modifier.fillMaxSize().padding(pad), contentPadding = PaddingValues(12.dp, 12.dp, 12.dp, 88.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            item { Text(stringResource(R.string.sync_intro), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            items(pairs, key = { it.id }) { p -> PairCard(p, statuses[p.id] ?: PairStatus(), go) }
        }
    }
}

@Composable
private fun PairCard(p: SyncPair, s: PairStatus, go: (String) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    Card(Modifier.fillMaxWidth().clickable { go("pair:${p.id}") }) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Default.FolderShared, null, tint = MaterialTheme.colorScheme.primary)
                Text(SyncWorker.labelOf(p), Modifier.weight(1f).padding(start = 8.dp), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                val (label, tone) = stateLabel(p, s)
                StatusBadge(label, tone)
            }
            Text(stringResource(R.string.sync_phone_line, p.localLabel, modeLabel(p.mode)), style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (s.state == "syncing" && s.pending > 0) {
                LinearProgressIndicator(progress = { s.done.toFloat() / s.pending }, Modifier.fillMaxWidth())
                if (s.current.isNotEmpty()) Text(s.current, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (s.state == "error" && s.error.isNotEmpty()) Text(s.error, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
            s.brake?.let { BrakeBox(p, it) }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(if (s.lastSync > 0) stringResource(R.string.sync_last, formatTime(s.lastSync)) else "", Modifier.weight(1f), style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant)
                IconButton(onClick = {
                    scope.launch(Dispatchers.IO) {
                        Graph.syncDb.savePair(p.copy(paused = !p.paused))
                        SyncScheduler.apply(ctx)
                        SyncLive.restart(ctx)
                    }
                }) { Icon(if (p.paused) Icons.Default.PlayArrow else Icons.Default.Pause, stringResource(if (p.paused) R.string.sync_resume else R.string.sync_pause)) }
                IconButton(onClick = { SyncScheduler.runNow(ctx, p, manual = true) }, enabled = !p.paused) { Icon(Icons.Default.Sync, stringResource(R.string.sync_now)) }
            }
        }
    }
}

@Composable
private fun BrakeBox(p: SyncPair, b: io.github.nathplay15.strataos.sync.Brake) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(stringResource(R.string.sync_brake_text, SyncWorker.labelOf(p), b.localDeletes, b.remoteDeletes), style = MaterialTheme.typography.bodySmall)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { scope.launch(Dispatchers.IO) { SyncScheduler.resolveBrake(ctx, p.id, "restore") } }) { Text(stringResource(R.string.sync_brake_restore)) }
            TextButton(onClick = { scope.launch(Dispatchers.IO) { SyncScheduler.resolveBrake(ctx, p.id, "apply") } }) {
                Text(stringResource(R.string.sync_brake_apply), color = MaterialTheme.colorScheme.error)
            }
        }
    }
}

@Composable
private fun PairDetail(p: SyncPair, s: PairStatus, onBack: () -> Unit, go: (String) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var menu by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf(false) }
    var tick by remember { mutableIntStateOf(0) }
    val conflicts = rememberLoader(p.id, tick, s.lastSync) { Graph.syncDb.conflicts(p.id) }
    AreaScreen(SyncWorker.labelOf(p), onBack, subtitle = p.localLabel, actions = {
        IconButton(onClick = { SyncScheduler.runNow(ctx, p, manual = true) }, enabled = !p.paused) { Icon(Icons.Default.Sync, stringResource(R.string.sync_now)) }
        Box {
            IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.b_more)) }
            DropdownMenu(menu, { menu = false }) {
                DropdownMenuItem({ Text(stringResource(R.string.sync_edit)) }, { menu = false; go("edit:${p.id}") })
                DropdownMenuItem({ Text(stringResource(R.string.sync_remove)) }, { menu = false; removing = true })
            }
        }
    }) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                SectionCard {
                    val (label, tone) = stateLabel(p, s)
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        StatusBadge(label, tone)
                        if (s.lastSync > 0) Text(stringResource(R.string.sync_last, formatTime(s.lastSync)), style = MaterialTheme.typography.bodySmall)
                    }
                    if (s.state == "syncing" && s.pending > 0) {
                        LinearProgressIndicator(progress = { s.done.toFloat() / s.pending }, Modifier.fillMaxWidth())
                        Text(stringResource(R.string.sync_progress, s.done, s.pending, s.current), style = MaterialTheme.typography.bodySmall)
                    }
                    if (s.state == "error" && s.error.isNotEmpty()) Text(s.error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                    s.brake?.let { BrakeBox(p, it) }
                    KeyValue(stringResource(R.string.sync_nas_folder), p.remote.removePrefix("/srv/strata"))
                    KeyValue(stringResource(R.string.sync_phone_folder), p.localLabel)
                    KeyValue(stringResource(R.string.sync_direction), modeLabel(p.mode))
                    KeyValue(stringResource(R.string.sync_files), s.files.toString())
                    if (p.exclude.isNotEmpty()) KeyValue(stringResource(R.string.sync_excluded), p.exclude.joinToString(", "))
                    SwitchLine(stringResource(R.string.sync_pause), p.paused) { on ->
                        scope.launch(Dispatchers.IO) {
                            Graph.syncDb.savePair(p.copy(paused = on))
                            SyncScheduler.apply(ctx)
                            SyncLive.restart(ctx)
                        }
                    }
                }
                SectionCard(stringResource(R.string.sync_conflicts)) {
                    LoadedContent(conflicts) { list ->
                        if (list.isEmpty()) Text(stringResource(R.string.sync_no_conflicts), style = MaterialTheme.typography.bodySmall)
                        else {
                            Text(stringResource(R.string.sync_conflicts_help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            list.take(100).forEach { c ->
                                Column(Modifier.padding(vertical = 4.dp)) {
                                    Text(c.path, style = MaterialTheme.typography.bodyMedium)
                                    Text("→ " + c.copy.substringAfterLast('/') + " · " + formatTime(c.at), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                                HorizontalDivider()
                            }
                            TextButton(onClick = { scope.launch(Dispatchers.IO) { Graph.syncDb.clearConflicts(p.id); tick++ } }) { Text(stringResource(R.string.sync_conflicts_clear)) }
                        }
                    }
                }
                if (s.skipped.isNotEmpty() || s.links > 0) SectionCard(stringResource(R.string.sync_skipped)) {
                    if (s.links > 0) Text(pluralStringResource(R.plurals.sync_links, s.links, s.links), style = MaterialTheme.typography.bodySmall)
                    s.skipped.forEach { k ->
                        Column(Modifier.padding(vertical = 2.dp)) {
                            Text(k.path, style = MaterialTheme.typography.bodyMedium)
                            Text(k.reason, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
                Text(stringResource(R.string.sync_trash_help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
    if (removing) Confirm(stringResource(R.string.sync_remove), stringResource(R.string.sync_remove_text), stringResource(R.string.sync_remove),
        onConfirm = {
            scope.launch {
                withContext(Dispatchers.IO) { SyncScheduler.remove(ctx, p.id) }
                SyncLive.restart(ctx)
                onBack()
            }
        }, onDismiss = { removing = false })
}

/** Adds a pair ([pair] null) or changes one (its folders stay: another folder is another pair). */
@Composable
private fun PairEditor(pair: SyncPair?, onDone: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var remote by rememberSaveable { mutableStateOf(pair?.remote ?: "") }
    var writable by rememberSaveable { mutableStateOf(true) }
    var local by rememberSaveable { mutableStateOf(pair?.local ?: "") }
    var localLabel by rememberSaveable { mutableStateOf(pair?.localLabel ?: "") }
    var mode by rememberSaveable { mutableStateOf(pair?.mode ?: SyncMode.BOTH) }
    var wifi by rememberSaveable { mutableStateOf(pair?.wifiOnly ?: true) }
    var charging by rememberSaveable { mutableStateOf(pair?.chargingOnly ?: false) }
    var exclude by rememberSaveable { mutableStateOf(pair?.exclude?.joinToString("\n") ?: "") }
    var picking by remember { mutableStateOf(false) }
    var appName by rememberSaveable { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    val used = stringResource(R.string.sync_folder_used)
    val pickTree = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri: Uri? ->
        if (uri == null) return@rememberLauncherForActivityResult
        runCatching { ctx.contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION) }
        local = uri.toString()
        localLabel = treeLabel(uri)
        error = null
    }
    val editing = pair != null
    AreaScreen(stringResource(if (editing) R.string.sync_edit else R.string.sync_add), onDone) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                SectionCard(stringResource(R.string.sync_nas_folder)) {
                    Text(remote.removePrefix("/srv/strata").ifEmpty { stringResource(R.string.sync_choose) }, style = MaterialTheme.typography.bodyLarge)
                    if (!editing) OutlinedButton(onClick = { picking = true }) { Text(stringResource(R.string.sync_choose_nas)) }
                    if (!writable && mode != SyncMode.DOWN) Text(stringResource(R.string.sync_read_only), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                }
                SectionCard(stringResource(R.string.sync_phone_folder)) {
                    Text(localLabel.ifEmpty { stringResource(R.string.sync_choose) }, style = MaterialTheme.typography.bodyLarge)
                    if (!editing) {
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(onClick = { pickTree.launch(null) }) { Text(stringResource(R.string.sync_choose_phone)) }
                            TextButton(onClick = { appName = remote.substringAfterLast('/').ifEmpty { "Sync" } }) { Text(stringResource(R.string.sync_app_folder)) }
                        }
                        Text(stringResource(R.string.sync_phone_help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    } else Text(stringResource(R.string.sync_folders_fixed), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                SectionCard(stringResource(R.string.sync_direction)) {
                    listOf(SyncMode.BOTH to R.string.sync_mode_both_help, SyncMode.UP to R.string.sync_mode_up_help, SyncMode.DOWN to R.string.sync_mode_down_help).forEach { (m, help) ->
                        Row(Modifier.fillMaxWidth().selectable(mode == m, role = Role.RadioButton) { mode = m }.padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                            RadioButton(mode == m, null)
                            Column(Modifier.padding(start = 8.dp)) {
                                Text(modeLabel(m), style = MaterialTheme.typography.bodyLarge)
                                Text(stringResource(help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                    }
                }
                SectionCard(stringResource(R.string.sync_when)) {
                    SwitchLine(stringResource(R.string.backup_wifi_only), wifi) { wifi = it }
                    SwitchLine(stringResource(R.string.backup_charging_only), charging) { charging = it }
                    Text(stringResource(R.string.sync_when_help), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                SectionCard(stringResource(R.string.sync_excluded)) {
                    Field(stringResource(R.string.sync_excluded_field), exclude, { exclude = it }, singleLine = false, help = stringResource(R.string.sync_excluded_help))
                }
                error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
                Button(enabled = remote.isNotEmpty() && local.isNotEmpty(), modifier = Modifier.fillMaxWidth(), onClick = {
                    scope.launch {
                        val p = (pair ?: SyncPair(UUID.randomUUID().toString(), remote, local, localLabel)).copy(
                            mode = mode, wifiOnly = wifi, chargingOnly = charging,
                            exclude = exclude.split('\n', ',').map { it.trim().trim('/') }.filter { it.isNotEmpty() },
                        )
                        val clash = withContext(Dispatchers.IO) { Graph.syncDb.pairs().any { it.id != p.id && it.local == p.local } }
                        if (clash) {
                            error = used
                            return@launch
                        }
                        withContext(Dispatchers.IO) {
                            Graph.syncDb.savePair(p)
                            SyncScheduler.apply(ctx)
                            SyncScheduler.runNow(ctx, p, manual = true)
                        }
                        SyncLive.restart(ctx)
                        onDone()
                    }
                }) { Text(stringResource(if (editing) R.string.b_save else R.string.sync_start)) }
            }
        }
    }
    if (picking) NasFolderPicker(onCancel = { picking = false }) { path, w ->
        remote = path
        writable = w
        picking = false
    }
    appName?.let { n ->
        var name by remember { mutableStateOf(n) }
        AlertDialog(onDismissRequest = { appName = null }, title = { Text(stringResource(R.string.sync_app_folder)) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(stringResource(R.string.sync_app_folder_help), style = MaterialTheme.typography.bodySmall)
                    Field(stringResource(R.string.sync_app_folder_name), name, { name = it })
                }
            },
            confirmButton = {
                val clean = name.trim().replace('/', '-')
                TextButton(enabled = clean.isNotEmpty() && clean != "." && clean != "..", onClick = {
                    local = APP_PREFIX + clean
                    localLabel = "Android/data/${ctx.packageName}/files/Sync/$clean"
                    appName = null
                }) { Text(stringResource(R.string.b_ok)) }
            },
            dismissButton = { TextButton(onClick = { appName = null }) { Text(stringResource(R.string.b_cancel)) } })
    }
}

/** A readable name of a folder chosen with the system picker ("Documents/Notes", "SD card 1234-ABCD/Music"). */
fun treeLabel(uri: Uri): String {
    val id = runCatching { DocumentsContract.getTreeDocumentId(uri) }.getOrDefault("")
    val (vol, path) = id.split(':', limit = 2).let { it[0] to it.getOrElse(1) { "" } }
    return when {
        id.isEmpty() -> uri.toString()
        vol == "primary" -> path.ifEmpty { "/" }
        ':' in id -> "$vol/$path".trimEnd('/')
        else -> id
    }
}

/**
 * Picks a folder to follow on the NAS: a shared folder (from the Sync API)
 * or a folder inside one. [onPick] gets its path and whether it is writable.
 */
@Composable
fun NasFolderPicker(onCancel: () -> Unit, onPick: (String, Boolean) -> Unit) {
    var share by remember { mutableStateOf<SyncShare?>(null) }
    var path by remember { mutableStateOf("") }
    val shares = rememberLoader { Graph.api()!!.syncShares() }
    val list = rememberLoader(path) { if (path.isEmpty()) null else Graph.api()!!.files(path) }
    AlertDialog(
        onDismissRequest = onCancel,
        title = { Text(stringResource(R.string.sync_choose_nas)) },
        text = {
            Column {
                Text(path.removePrefix("/srv/strata").ifEmpty { stringResource(R.string.sync_shares) }, style = MaterialTheme.typography.labelLarge)
                Column(Modifier.heightIn(min = 120.dp, max = 400.dp)) {
                    if (share == null) LoadedContent(shares) { l ->
                        LazyColumn {
                            items(l, key = { it.path }) { s ->
                                ListItem(headlineContent = { Text(s.name) }, supportingContent = { if (!s.writable) Text(stringResource(R.string.sync_share_read_only)) },
                                    leadingContent = { Icon(Icons.Default.FolderShared, null) },
                                    modifier = Modifier.fillMaxWidth().clickable { share = s; path = s.path })
                            }
                        }
                    } else LoadedContent(list) { l ->
                        LazyColumn {
                            item {
                                ListItem(headlineContent = { Text("..") }, leadingContent = { Icon(Icons.AutoMirrored.Filled.ArrowBack, null) },
                                    modifier = Modifier.clickable { if (path == share!!.path) { share = null; path = "" } else path = path.substringBeforeLast('/') })
                            }
                            items(l?.entries.orEmpty().filter { it.dir }, key = { it.path }) { e ->
                                ListItem(headlineContent = { Text(e.name) }, leadingContent = { Icon(Icons.Default.Folder, null) },
                                    modifier = Modifier.fillMaxWidth().clickable { path = e.path })
                            }
                        }
                    }
                }
            }
        },
        confirmButton = { TextButton(enabled = share != null, onClick = { onPick(path, share!!.writable) }) { Text(stringResource(R.string.choose_folder)) } },
        dismissButton = { TextButton(onClick = onCancel) { Text(stringResource(R.string.cancel)) } },
    )
}
