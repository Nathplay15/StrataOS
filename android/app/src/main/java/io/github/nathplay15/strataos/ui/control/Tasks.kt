package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.EventRepeat
import androidx.compose.material.icons.filled.Bolt
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.backupSnapshots
import io.github.nathplay15.strataos.net.manage.backups
import io.github.nathplay15.strataos.net.manage.deleteBackup
import io.github.nathplay15.strataos.net.manage.deleteRoutine
import io.github.nathplay15.strataos.net.manage.deleteSchedule
import io.github.nathplay15.strataos.net.manage.fireRoutine
import io.github.nathplay15.strataos.net.manage.objs
import io.github.nathplay15.strataos.net.manage.resyncBackup
import io.github.nathplay15.strataos.net.manage.routines
import io.github.nathplay15.strataos.net.manage.runBackup
import io.github.nathplay15.strataos.net.manage.runSchedule
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.net.manage.saveBackup
import io.github.nathplay15.strataos.net.manage.saveRoutine
import io.github.nathplay15.strataos.net.manage.saveSchedule
import io.github.nathplay15.strataos.net.manage.schedules
import io.github.nathplay15.strataos.net.manage.sendObj
import io.github.nathplay15.strataos.net.manage.with
import io.github.nathplay15.strataos.net.manage.a
import io.github.nathplay15.strataos.ui.LocalOpenWeb
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import kotlinx.serialization.json.JsonObject

// Backups, scheduled tasks and routines: the lists of the web pages with
// their quick actions (run, on/off, delete). Creating and editing a task
// (many destinations and options) opens that page of the web interface.

private class Pending(val title: String, val text: String, val confirm: String, val destructive: Boolean, val run: () -> Unit)

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun BackupsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 8000) { it.backups() }
    val actions = rememberActions()
    val r = androidx.compose.ui.platform.LocalResources.current
    val web = LocalOpenWeb.current
    var pending by remember { mutableStateOf<Pending?>(null) }
    var snaps by remember { mutableStateOf<JsonObject?>(null) }
    AreaScreen(stringResource(R.string.ctl_backups), onBack, actions = {
        TextButton(onClick = { web("/backup", null) }) { Text(stringResource(R.string.ctl_restore_web)) }
    }) { pad ->
        Refreshable(data, pad) { list ->
            if (list.isEmpty()) Empty(Icons.Default.Backup, stringResource(R.string.ctl_no_backups)) { OutlinedButton(onClick = { web("/backup", null) }) { Text(stringResource(R.string.ctl_create_web)) } }
            else ScrollColumn {
                list.forEach { bk ->
                    val last = bk.objs("history").firstOrNull()
                    val running = bk.b("running")
                    SectionCard {
                        CardHead(bk.s("name"), "${bk.s("kind")} → ${bk.s("destType")}") {
                            Switch(bk.b("enabled"), enabled = !actions.busy, onCheckedChange = { on -> actions.run(after = data.reload) { it.saveBackup(bk.with("enabled", on)) } })
                        }
                        KeyValue(stringResource(R.string.ctl_sources), bk["sources"].a().joinToString(", ") { (it as? kotlinx.serialization.json.JsonPrimitive)?.content.orEmpty() })
                        KeyValue(stringResource(R.string.ctl_destination), bk.s("destPath").ifEmpty { bk.s("host") })
                        KeyValue(stringResource(R.string.ctl_next_run), if (bk.b("enabled")) whenText(bk.s("nextRun")).ifEmpty { stringResource(R.string.ctl_on_demand) } else stringResource(R.string.usr_disabled))
                        RunBadge(last, running)
                        if (last != null) Text(whenText(last.s("started")) + " — " + last.s("message"), style = MaterialTheme.typography.bodySmall)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            if (running && bk.s("jobId").isNotEmpty()) OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.sendObj("POST", "/api/jobs/${bk.s("jobId")}/cancel") } }) { Text(stringResource(R.string.ctl_stop)) }
                            else OutlinedButton(enabled = !actions.busy && !running, onClick = { actions.run(done = r.getString(R.string.ctl_started), after = data.reload) { it.runBackup(bk.s("id")) } }) { Text(stringResource(R.string.ctl_run)) }
                            if (bk.s("kind") == "restic") TextButton(onClick = { snaps = bk }) { Text(stringResource(R.string.ctl_versions)) }
                            if (bk.s("kind") == "sync" && bk.b("synced")) TextButton(enabled = !running, onClick = {
                                pending = Pending(r.getString(R.string.ctl_resync_title, bk.s("name")), r.getString(R.string.ctl_resync_text), r.getString(R.string.ctl_resync), false) { actions.run(after = data.reload) { it.resyncBackup(bk.s("id")) } }
                            }) { Text(stringResource(R.string.ctl_resync)) }
                            TextButton(onClick = {
                                pending = Pending(r.getString(R.string.ctl_delete_title, bk.s("name")), r.getString(R.string.ctl_delete_backup_text), r.getString(R.string.b_delete), true) { actions.run(after = data.reload) { it.deleteBackup(bk.s("id")) } }
                            }) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
                        }
                    }
                }
                Hint(stringResource(R.string.ctl_backups_hint))
            }
        }
    }
    pending?.let { p -> Confirm(p.title, p.text, p.confirm, onConfirm = p.run, onDismiss = { pending = null }, destructive = p.destructive) }
    snaps?.let { bk -> SnapshotsDialog(bk) { snaps = null } }
}

@Composable
private fun SnapshotsDialog(bk: JsonObject, onClose: () -> Unit) {
    val data = rememberData(bk.s("id")) { it.backupSnapshots(bk.s("id")) }
    androidx.compose.material3.AlertDialog(
        onDismissRequest = onClose,
        title = { Text(bk.s("name")) },
        text = {
            androidx.compose.foundation.layout.Column(androidx.compose.ui.Modifier.verticalScroll(androidx.compose.foundation.rememberScrollState()), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                when (val st = data.state) {
                    is io.github.nathplay15.strataos.ui.Loaded.Ok -> if (st.value.isEmpty()) Text(stringResource(R.string.ctl_no_versions)) else st.value.forEach { s ->
                        Text(whenText(s.s("time")) + "  ·  " + s.s("short_id"), style = MaterialTheme.typography.bodyMedium)
                    }
                    is io.github.nathplay15.strataos.ui.Loaded.Failed -> Text(io.github.nathplay15.strataos.ui.errorText(androidx.compose.ui.platform.LocalContext.current, st.error), color = MaterialTheme.colorScheme.error)
                    else -> androidx.compose.material3.LinearProgressIndicator()
                }
                Hint(stringResource(R.string.ctl_restore_hint))
            }
        },
        confirmButton = { TextButton(onClick = onClose) { Text(stringResource(R.string.b_ok)) } },
    )
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun SchedulesScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 10000) { it.schedules() }
    val actions = rememberActions()
    val r = androidx.compose.ui.platform.LocalResources.current
    val web = LocalOpenWeb.current
    var pending by remember { mutableStateOf<Pending?>(null) }
    AreaScreen(stringResource(R.string.ctl_schedules), onBack, actions = {
        TextButton(onClick = { web("/system/schedules", null) }) { Text(stringResource(R.string.ctl_new_web)) }
    }) { pad ->
        Refreshable(data, pad) { list ->
            if (list.isEmpty()) Empty(Icons.Default.EventRepeat, stringResource(R.string.ctl_no_schedules))
            else ScrollColumn {
                list.forEach { t ->
                    val running = t.b("running")
                    SectionCard {
                        CardHead(t.s("name"), "${t.s("type")} · ${t.s("target")}") {
                            Switch(t.b("enabled"), enabled = !actions.busy, onCheckedChange = { on -> actions.run(after = data.reload) { it.saveSchedule(t.with("enabled", on)) } })
                        }
                        KeyValue(stringResource(R.string.ctl_when), t.s("cron"), mono = true)
                        KeyValue(stringResource(R.string.ctl_next_run), if (t.b("enabled")) whenText(t.s("nextRun")) else stringResource(R.string.usr_disabled))
                        val last = t.objs("history").firstOrNull()
                        RunBadge(last, running)
                        if (last != null) Text(whenText(last.s("started")) + " — " + last.s("message"), style = MaterialTheme.typography.bodySmall)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(enabled = !actions.busy && !running, onClick = { actions.run(done = r.getString(R.string.ctl_started), after = data.reload) { it.runSchedule(t.s("id")) } }) { Text(stringResource(R.string.ctl_run)) }
                            TextButton(onClick = {
                                pending = Pending(r.getString(R.string.ctl_delete_title, t.s("name")), r.getString(R.string.ctl_delete_task_text), r.getString(R.string.b_delete), true) { actions.run(after = data.reload) { it.deleteSchedule(t.s("id")) } }
                            }) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
                        }
                    }
                }
            }
        }
    }
    pending?.let { p -> Confirm(p.title, p.text, p.confirm, onConfirm = p.run, onDismiss = { pending = null }, destructive = p.destructive) }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun RoutinesScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 10000) { it.routines() }
    val actions = rememberActions()
    val r = androidx.compose.ui.platform.LocalResources.current
    val web = LocalOpenWeb.current
    var pending by remember { mutableStateOf<Pending?>(null) }
    AreaScreen(stringResource(R.string.ctl_routines), onBack, actions = {
        TextButton(onClick = { web("/system/routines", null) }) { Text(stringResource(R.string.ctl_new_web)) }
    }) { pad ->
        Refreshable(data, pad) { list ->
            if (list.isEmpty()) Empty(Icons.Default.Bolt, stringResource(R.string.ctl_no_routines))
            else ScrollColumn {
                list.forEach { rt ->
                    SectionCard {
                        CardHead(rt.s("name"), rt.s("trigger") + (rt.s("value").takeIf { it.isNotEmpty() && it != "0" }?.let { " · $it" } ?: "") + (rt.s("target").takeIf { it.isNotEmpty() }?.let { " · $it" } ?: "")) {
                            Switch(rt.b("enabled"), enabled = !actions.busy, onCheckedChange = { on -> actions.run(after = data.reload) { it.saveRoutine(rt.with("enabled", on)) } })
                        }
                        KeyValue(stringResource(R.string.ctl_actions), rt.objs("actions").joinToString(", ") { a -> a.s("kind") + (a.s("target").takeIf { it.isNotEmpty() }?.let { " $it" } ?: "") })
                        KeyValue(stringResource(R.string.ctl_last_fired), whenText(rt.s("lastFired")).ifEmpty { stringResource(R.string.ctl_never) })
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(enabled = !actions.busy, onClick = {
                                pending = Pending(r.getString(R.string.ctl_test_title, rt.s("name")), r.getString(R.string.ctl_test_text), r.getString(R.string.ctl_test), false) { actions.run(done = r.getString(R.string.ctl_test_done), after = data.reload) { it.fireRoutine(rt.s("id")) } }
                            }) { Text(stringResource(R.string.ctl_test)) }
                            TextButton(onClick = {
                                pending = Pending(r.getString(R.string.ctl_delete_title, rt.s("name")), r.getString(R.string.ctl_delete_routine_text), r.getString(R.string.b_delete), true) { actions.run(after = data.reload) { it.deleteRoutine(rt.s("id")) } }
                            }) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
                        }
                    }
                }
            }
        }
    }
    pending?.let { p -> Confirm(p.title, p.text, p.confirm, onConfirm = p.run, onDismiss = { pending = null }, destructive = p.destructive) }
}
