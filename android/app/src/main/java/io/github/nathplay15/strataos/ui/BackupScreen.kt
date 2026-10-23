package io.github.nathplay15.strataos.ui

import android.Manifest
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.ui.semantics.Role
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.work.WorkInfo
import androidx.work.WorkManager
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.backup.BackupScheduler
import io.github.nathplay15.strataos.backup.BackupSettings
import io.github.nathplay15.strataos.backup.MediaScanner

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun BackupScreen() {
    val ctx = LocalContext.current
    val s = remember { BackupSettings(ctx) }
    var tick by remember { mutableIntStateOf(0) } // settings changed: recompose
    var permission by remember { mutableStateOf(MediaScanner.hasPermission(ctx)) }
    var picking by remember { mutableStateOf(false) }
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { permission = MediaScanner.hasPermission(ctx) }
    val askNotif = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { }
    val albums = rememberLoader(permission) { if (permission) MediaScanner.albums(ctx) else emptyList() }
    val work by remember { WorkManager.getInstance(ctx).getWorkInfosByTagFlow("backup") }.collectAsState(initial = emptyList())
    val running = work.firstOrNull { it.state == WorkInfo.State.RUNNING }
    fun changed() { tick++; BackupScheduler.apply(ctx) }

    Column(Modifier.fillMaxSize()) {
        TopAppBar(title = { Text(stringResource(R.string.backup_title)) })
        key(tick) {
            Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text(stringResource(R.string.backup_intro), style = MaterialTheme.typography.bodyMedium)
                if (!permission) {
                    Card { Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(stringResource(R.string.backup_permission))
                        Button(onClick = { ask.launch(MediaScanner.permissions()) }) { Text(stringResource(R.string.allow)) }
                    } }
                    return@Column
                }
                SwitchRow(stringResource(R.string.backup_enable), s.enabled) {
                    s.enabled = it
                    if (it && Build.VERSION.SDK_INT >= 33) askNotif.launch(Manifest.permission.POST_NOTIFICATIONS)
                    changed()
                }
                ListItem(headlineContent = { Text(stringResource(R.string.backup_destination)) },
                    supportingContent = { Text(s.destination.removePrefix("/srv/strata").ifEmpty { stringResource(R.string.backup_choose_destination) }) },
                    trailingContent = { OutlinedButton(onClick = { picking = true }) { Text(stringResource(R.string.change)) } })
                SwitchRow(stringResource(R.string.backup_wifi_only), s.wifiOnly) { s.wifiOnly = it; changed() }
                SwitchRow(stringResource(R.string.backup_charging_only), s.chargingOnly) { s.chargingOnly = it; changed() }
                SwitchRow(stringResource(R.string.backup_videos), s.videos) { s.videos = it; changed() }
                Text(stringResource(R.string.backup_albums), style = MaterialTheme.typography.titleMedium)
                LoadedContent(albums) { list ->
                    Card { Column {
                        list.forEach { a ->
                            Row(Modifier.fillMaxWidth().toggleable(a.id in s.buckets, role = Role.Checkbox) { on -> s.buckets = if (on) s.buckets + a.id else s.buckets - a.id; changed() }
                                .padding(horizontal = 8.dp, vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                                Checkbox(a.id in s.buckets, null)
                                Text(a.name, Modifier.weight(1f))
                                Text("${a.count}", style = MaterialTheme.typography.bodySmall)
                            }
                        }
                    } }
                }
                if (running != null) {
                    val p = running.progress
                    val done = p.getInt("done", 0)
                    val total = p.getInt("total", 0)
                    Text(stringResource(R.string.backup_progress, done, total) + (p.getString("file")?.let { " · $it" } ?: ""))
                    LinearProgressIndicator(progress = { if (total > 0) done.toFloat() / total else 0f }, modifier = Modifier.fillMaxWidth())
                }
                Button(enabled = s.enabled && s.destination.isNotEmpty() && s.buckets.isNotEmpty(), onClick = { BackupScheduler.runNow(ctx) }, modifier = Modifier.fillMaxWidth()) {
                    Text(stringResource(R.string.backup_now))
                }
                if (s.lastResult.isNotEmpty()) Text(
                    (if (s.lastRun > 0) formatTime(s.lastRun) + " · " else "") + s.lastResult,
                    style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Text(stringResource(R.string.backup_count, Graph.backupDb.doneCount()), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Text(stringResource(R.string.backup_never_deletes), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }

    if (picking) FolderPicker(stringResource(R.string.backup_destination), start = s.destination, onCancel = { picking = false }, onPick = {
        if (it != s.destination) Graph.backupDb.reset() // another folder: the dedupe check decides again
        s.destination = it
        picking = false
        changed()
    })
}

@Composable
fun SwitchRow(label: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().toggleable(checked, role = Role.Switch, onValueChange = onChange).padding(horizontal = 16.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(label, Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
        Switch(checked, null)
    }
}

@Composable
private fun key(k: Any, content: @Composable () -> Unit) = androidx.compose.runtime.key(k) { content() }
