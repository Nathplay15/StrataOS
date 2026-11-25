package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.clickable
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.a
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.json
import io.github.nathplay15.strataos.net.manage.jsonList
import io.github.nathplay15.strataos.net.manage.l
import io.github.nathplay15.strataos.net.manage.o
import io.github.nathplay15.strataos.net.manage.objs
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.net.manage.sendObj
import io.github.nathplay15.strataos.net.manage.startJob
import io.github.nathplay15.strataos.net.manage.with
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

// Data coming in and going out: the migration assistant (import from another
// NAS), cloud sync (rclone), Time Machine destinations and backup vaults.
// Same routes, objects round-tripped like the web ({...task, enabled}).

private class Ask2(val title: String, val text: String, val confirm: String, val run: () -> Unit)

/** The schedule field shared by the editors: chips, cron text and the NAS's preview. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ScheduleCard(cron: String, presets: List<Pair<String, Int>>, onChange: (String) -> Unit) {
    SectionCard(stringResource(R.string.ctl_when)) {
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            presets.forEach { (c, l) -> androidx.compose.material3.FilterChip(cron == c, { onChange(c) }, label = { Text(stringResource(l)) }) }
        }
        Field(stringResource(R.string.ctl_cron), cron, onChange, help = stringResource(R.string.ctl_cron_help))
    }
}

// ---- Migration assistant ----

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun MigrationScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 10000) { it.jsonList("/api/migrations") }
    val actions = rememberActions()
    val r = LocalResources.current
    var edit by remember { mutableStateOf<JsonObject?>(null) }
    var ask by remember { mutableStateOf<Ask2?>(null) }
    edit?.let { m -> MigrationEditor(m) { edit = null; data.reload() }; return }
    AreaScreen(stringResource(R.string.ctl_w_migration), onBack, actions = {
        IconButton(onClick = { edit = JsonObject(emptyMap()).put("kind" to "smb", "enabled" to true) }) { Icon(Icons.Default.Add, stringResource(R.string.tr_new_import)) }
    }) { pad ->
        Refreshable(data, pad) { list ->
            if (list.isEmpty()) Empty(Icons.Default.Add, stringResource(R.string.tr_no_imports)) { Button(onClick = { edit = JsonObject(emptyMap()).put("kind" to "smb", "enabled" to true) }) { Text(stringResource(R.string.tr_new_import)) } }
            else ScrollColumn {
                Hint(stringResource(R.string.tr_migration_help))
                list.forEach { m ->
                    SectionCard {
                        CardHead(m.s("name"), "${m.s("kind")}://${m.s("host")}/${m.s("share")} → ${m.s("dest")}") {
                            Switch(m.b("enabled"), enabled = !actions.busy, onCheckedChange = { on -> actions.run(after = data.reload) { it.sendObj("PUT", "/api/migrations/${m.s("id")}", m.with("enabled", on)) } })
                        }
                        val last = m.objs("history").firstOrNull()
                        RunBadge(last, m.b("running"))
                        if (last != null) Text(whenText(last.s("started")) + " — " + last.s("message"), style = MaterialTheme.typography.bodySmall)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(enabled = !actions.busy && !m.b("running"), onClick = { actions.run(done = r.getString(R.string.ctl_started), after = data.reload) { it.sendObj("POST", "/api/migrations/${m.s("id")}/run") } }) { Text(stringResource(R.string.ctl_run)) }
                            TextButton(onClick = { edit = m }) { Text(stringResource(R.string.ctl_edit)) }
                            TextButton(onClick = { ask = Ask2(r.getString(R.string.ctl_delete_title, m.s("name")), r.getString(R.string.tr_delete_import_text), r.getString(R.string.b_delete)) { actions.run(after = data.reload) { it.sendObj("DELETE", "/api/migrations/${m.s("id")}") } } }) {
                                Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error)
                            }
                        }
                    }
                }
            }
        }
    }
    ask?.let { a -> Confirm(a.title, a.text, a.confirm, onConfirm = a.run, onDismiss = { ask = null }) }
}

@Composable
private fun MigrationEditor(m: JsonObject, onDone: () -> Unit) {
    val actions = rememberActions()
    val r = LocalResources.current
    val shares = rememberData(Unit) { it.jsonList("/api/shares") }
    var name by remember { mutableStateOf(m.s("name")) }
    var kind by remember { mutableStateOf(m.s("kind").ifEmpty { "smb" }) }
    var host by remember { mutableStateOf(m.s("host")) }
    var share by remember { mutableStateOf(m.s("share")) }
    var path by remember { mutableStateOf(m.s("path")) }
    var user by remember { mutableStateOf(m.s("user")) }
    var pass by remember { mutableStateOf(m.s("password")) }
    var dest by remember { mutableStateOf(m.s("dest")) }
    var del by remember { mutableStateOf(m.b("delete")) }
    var cron by remember { mutableStateOf(m.s("cron")) }
    AreaScreen(if (m.s("id").isEmpty()) stringResource(R.string.tr_new_import) else r.getString(R.string.ctl_edit_title, m.s("name")), onDone) { pad ->
        Refreshable(shares, pad) { sh ->
            ScrollColumn {
                SectionCard(stringResource(R.string.tr_source)) {
                    Field(stringResource(R.string.name), name, { name = it })
                    Choice(stringResource(R.string.tr_protocol), listOf("smb" to "SMB (Windows, Synology, QNAP…)", "nfs" to "NFS", "rsync" to "rsync (SSH)"), kind, { kind = it })
                    Field(stringResource(R.string.ctl_host), host, { host = it.trim() })
                    Field(stringResource(if (kind == "nfs") R.string.tr_export else R.string.tr_share), share, { share = it.trim() })
                    Field(stringResource(R.string.tr_subfolder), path, { path = it.trim() })
                    if (kind != "nfs") {
                        Field(stringResource(R.string.usr_username), user, { user = it.trim() })
                        Field(stringResource(R.string.usr_password), pass, { pass = it }, secret = true)
                    }
                }
                SectionCard(stringResource(R.string.ctl_destination)) {
                    Choice(stringResource(R.string.ctl_dest_folder), sh.map { it.s("path") to "${it.s("name")} (${it.s("path")})" } + listOfNotNull(dest.takeIf { d -> d.isNotEmpty() && sh.none { it.s("path") == d } }?.let { it to it }), dest, { dest = it })
                    Field(stringResource(R.string.ctl_dest_folder), dest, { dest = it.trim() })
                    SwitchLine(stringResource(R.string.ctl_mirror_delete), del) { del = it }
                }
                ScheduleCard(cron, listOf("" to R.string.ctl_on_demand, "0 1 * * *" to R.string.ctl_cron_daily, "0 * * * *" to R.string.ctl_cron_hourly)) { cron = it }
                Button(enabled = !actions.busy && name.isNotBlank() && host.isNotBlank() && dest.isNotBlank(), onClick = {
                    val body = m.put("name" to name.trim(), "kind" to kind, "host" to host, "share" to share, "path" to path, "user" to user, "password" to pass, "dest" to dest, "delete" to del, "cron" to cron.trim())
                    actions.run(done = r.getString(R.string.b_saved), after = onDone) { a -> if (m.s("id").isEmpty()) a.sendObj("POST", "/api/migrations", body) else a.sendObj("PUT", "/api/migrations/${m.s("id")}", body) }
                }) { Text(stringResource(R.string.b_save)) }
            }
        }
    }
}

// ---- Cloud sync (rclone) ----

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun CloudSyncScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 8000) { it.json("/api/cloudsync").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    var edit by remember { mutableStateOf<JsonObject?>(null) }
    var ask by remember { mutableStateOf<Ask2?>(null) }
    var job by remember { mutableStateOf<NasJob?>(null) }
    val providers = data.value?.objs("providers").orEmpty()
    edit?.let { t -> CloudEditor(t, providers) { edit = null; data.reload() }; return }
    fun blank(): JsonObject {
        val p = providers.firstOrNull() ?: JsonObject(emptyMap())
        val params = p.objs("fields").filter { it["options"] is JsonArray }.associate { it.s("key") to (it["options"].a().firstOrNull() ?: JsonPrimitive("")) }
        return JsonObject(emptyMap()).put("provider" to p.s("id"), "params" to JsonObject(params), "secrets" to JsonObject(emptyMap()), "direction" to "upload", "mirror" to false, "cron" to "0 */6 * * *", "enabled" to true)
    }
    AreaScreen(stringResource(R.string.ctl_w_cloudsync), onBack, actions = {
        if (data.value?.b("installed") == true) IconButton(onClick = { edit = blank() }) { Icon(Icons.Default.Add, stringResource(R.string.tr_new_sync)) }
    }) { pad ->
        Refreshable(data, pad) { d ->
            ScrollColumn {
                if (!d.b("installed")) SectionCard {
                    Hint(stringResource(R.string.tr_rclone_missing))
                    Button(enabled = !actions.busy, onClick = { actions.run { a -> job = a.startJob("POST", "/api/system/components/rclone/install") } }) { Text(stringResource(R.string.nas_install)) }
                }
                val tasks = d.objs("tasks")
                if (d.b("installed") && tasks.isEmpty()) SectionCard { Hint(stringResource(R.string.tr_no_syncs)); Button(onClick = { edit = blank() }) { Text(stringResource(R.string.tr_new_sync)) } }
                tasks.forEach { t ->
                    val label = providers.firstOrNull { it.s("id") == t.s("provider") }?.s("label") ?: t.s("provider")
                    SectionCard {
                        CardHead(t.s("name"), "${t.s("localPath")} ${if (t.s("direction") == "download") "←" else if (t.s("direction") == "bisync") "↔" else "→"} $label:${t.s("remotePath")}") {
                            Switch(t.b("enabled"), enabled = !actions.busy, onCheckedChange = { on -> actions.run(after = data.reload) { it.sendObj("PUT", "/api/cloudsync/${t.s("id")}", t.with("enabled", on)) } })
                        }
                        val last = t.objs("history").firstOrNull()
                        RunBadge(last, t.b("running"))
                        if (last != null) Text(whenText(last.s("started")) + " — " + last.s("message"), style = MaterialTheme.typography.bodySmall)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(enabled = !actions.busy && !t.b("running"), onClick = { actions.run(done = r.getString(R.string.ctl_started), after = data.reload) { it.sendObj("POST", "/api/cloudsync/${t.s("id")}/run") } }) { Text(stringResource(R.string.ctl_run)) }
                            if (t.s("direction") == "bisync" && t.b("synced")) TextButton(onClick = { ask = Ask2(r.getString(R.string.ctl_resync_title, t.s("name")), r.getString(R.string.ctl_resync_text), r.getString(R.string.ctl_resync)) { actions.run(after = data.reload) { it.sendObj("POST", "/api/cloudsync/${t.s("id")}/resync") } } }) { Text(stringResource(R.string.ctl_resync)) }
                            TextButton(onClick = { edit = t }) { Text(stringResource(R.string.ctl_edit)) }
                            TextButton(onClick = { ask = Ask2(r.getString(R.string.ctl_delete_title, t.s("name")), r.getString(R.string.tr_delete_sync_text), r.getString(R.string.b_delete)) { actions.run(after = data.reload) { it.sendObj("DELETE", "/api/cloudsync/${t.s("id")}") } } }) {
                                Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error)
                            }
                        }
                    }
                }
            }
        }
    }
    ask?.let { a -> Confirm(a.title, a.text, a.confirm, onConfirm = a.run, onDismiss = { ask = null }) }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

@Composable
private fun CloudEditor(t: JsonObject, providers: List<JsonObject>, onDone: () -> Unit) {
    val actions = rememberActions()
    val r = LocalResources.current
    val sources = rememberData(Unit) { it.json("/api/backups/sources").o().objs("folders") }
    var name by remember { mutableStateOf(t.s("name")) }
    var provider by remember { mutableStateOf(t.s("provider")) }
    val params = remember { mutableStateMapOf<String, String>().apply { t["params"].o().forEach { (k, v) -> put(k, (v as? JsonPrimitive)?.content.orEmpty()) } } }
    val secrets = remember { mutableStateMapOf<String, String>().apply { t["secrets"].o().forEach { (k, v) -> put(k, (v as? JsonPrimitive)?.content.orEmpty()) } } }
    var local by remember { mutableStateOf(t.s("localPath")) }
    var remote by remember { mutableStateOf(t.s("remotePath")) }
    var direction by remember { mutableStateOf(t.s("direction").ifEmpty { "upload" }) }
    var mirror by remember { mutableStateOf(t.b("mirror")) }
    var cron by remember { mutableStateOf(t.s("cron")) }
    val p = providers.firstOrNull { it.s("id") == provider }
    fun payload() = t.put("name" to name.trim(), "provider" to provider, "params" to JsonObject(params.mapValues { JsonPrimitive(it.value) }),
        "secrets" to JsonObject(secrets.mapValues { JsonPrimitive(it.value) }), "localPath" to local, "remotePath" to remote.trim(), "direction" to direction, "mirror" to mirror, "cron" to cron.trim())
    AreaScreen(if (t.s("id").isEmpty()) stringResource(R.string.tr_new_sync) else r.getString(R.string.ctl_edit_title, t.s("name")), onDone) { pad ->
        Refreshable(sources, pad) { folders ->
            ScrollColumn {
                SectionCard {
                    Field(stringResource(R.string.name), name, { name = it })
                    Choice(stringResource(R.string.net_ddns_provider), providers.map { it.s("id") to it.s("label") }, provider, { provider = it }, enabled = t.s("id").isEmpty())
                    p?.objs("fields")?.forEach { f ->
                        val k = f.s("key")
                        val opts = f["options"].a().map { (it as JsonPrimitive).content }
                        when {
                            opts.isNotEmpty() -> Choice(f.s("label"), opts.map { it to it }, params[k] ?: opts.first(), { params[k] = it })
                            f.b("secret") -> Field(f.s("label"), secrets[k].orEmpty(), { secrets[k] = it }, secret = true, help = f.s("hint").ifEmpty { null })
                            else -> Field(f.s("label"), params[k].orEmpty(), { params[k] = it.trim() }, help = f.s("hint").ifEmpty { null })
                        }
                    }
                    if (p?.s("authorize")?.isNotEmpty() == true) Hint(stringResource(R.string.tr_authorize_web))
                }
                SectionCard(stringResource(R.string.ctl_sources)) {
                    Choice(stringResource(R.string.tr_local_folder), folders.map { it.s("path") to "${it.s("path")} (${it.s("label")})" } + listOfNotNull(local.takeIf { l -> l.isNotEmpty() && folders.none { it.s("path") == l } }?.let { it to it }), local, { local = it })
                    Field(stringResource(R.string.tr_remote_folder), remote, { remote = it })
                    Choice(stringResource(R.string.tr_direction), listOf("upload" to r.getString(R.string.tr_upload), "download" to r.getString(R.string.tr_download), "bisync" to r.getString(R.string.tr_bisync)), direction, { direction = it })
                    if (direction != "bisync") SwitchLine(stringResource(R.string.ctl_mirror_delete), mirror) { mirror = it }
                }
                ScheduleCard(cron, listOf("0 */6 * * *" to R.string.tr_cron_6h, "0 1 * * *" to R.string.ctl_cron_daily, "" to R.string.ctl_on_demand)) { cron = it }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(enabled = !actions.busy, onClick = { actions.run { a -> actions.say(a.sendObj("POST", "/api/cloudsync/test", payload()).o().s("message").ifEmpty { r.getString(R.string.ctl_test_ok) }) } }) { Text(stringResource(R.string.ctl_test_dest)) }
                    Button(enabled = !actions.busy && name.isNotBlank() && local.isNotEmpty(), onClick = {
                        actions.run(done = r.getString(R.string.b_saved), after = onDone) { a -> if (t.s("id").isEmpty()) a.sendObj("POST", "/api/cloudsync", payload()) else a.sendObj("PUT", "/api/cloudsync/${t.s("id")}", payload()) }
                    }) { Text(stringResource(R.string.b_save)) }
                }
            }
        }
    }
}

// ---- Time Machine ----

@Composable
fun TimeMachineScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.jsonList("/api/backups/timemachine") }
    val actions = rememberActions()
    val r = LocalResources.current
    var creating by remember { mutableStateOf(false) }
    AreaScreen("Time Machine", onBack, actions = { IconButton(onClick = { creating = true }) { Icon(Icons.Default.Add, stringResource(R.string.tr_tm_create)) } }) { pad ->
        Refreshable(data, pad) { list ->
            ScrollColumn {
                Hint(stringResource(R.string.tr_tm_help))
                if (list.isEmpty()) SectionCard { Hint(stringResource(R.string.tr_tm_none)); Button(onClick = { creating = true }) { Text(stringResource(R.string.tr_tm_create)) } }
                list.forEach { s ->
                    var gb by remember(s) { mutableStateOf(s.l("maxGb").takeIf { it > 0 }?.toString() ?: "") }
                    SectionCard {
                        CardHead(s.s("name"), s.s("path")) { StatusBadge(formatBytes(s.l("used")), Tone.Neutral) }
                        s.objs("backups").forEach { b -> KeyValue(b.s("name").removeSuffix(".sparsebundle"), formatBytes(b.l("size")) + " · " + whenText(b.s("lastBackup"))) }
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            Field(stringResource(R.string.tr_tm_limit), gb, { gb = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
                            TextButton(enabled = !actions.busy, onClick = { actions.run(done = r.getString(R.string.b_saved), after = data.reload) { it.sendObj("PUT", "/api/backups/timemachine/${java.net.URLEncoder.encode(s.s("name"), "UTF-8").replace("+", "%20")}", JsonObject(emptyMap()).put("maxGb" to (gb.toIntOrNull() ?: 0))) } }) { Text(stringResource(R.string.b_save)) }
                        }
                    }
                }
            }
        }
    }
    if (creating) TmCreateDialog(onDismiss = { creating = false }) { body -> creating = false; actions.run(done = r.getString(R.string.tr_tm_created), after = data.reload) { it.sendObj("POST", "/api/backups/timemachine", body) } }
}

@Composable
private fun TmCreateDialog(onDismiss: () -> Unit, onCreate: (JsonObject) -> Unit) {
    val who = rememberData(Unit) { a -> a.jsonList("/api/users").filter { !it.b("disabled") }.map { it.s("username") } + a.jsonList("/api/groups").filter { !it.b("system") }.map { "@" + it.s("name") } }
    val vols = rememberData(Unit) { a -> a.json("/api/backups/sources").o().objs("folders").filter { it.s("label").startsWith("Volume") || it.s("label").startsWith("ZFS") }.map { it.s("path") } }
    var name by remember { mutableStateOf("TimeMachine") }
    var path by remember { mutableStateOf("") }
    var gb by remember { mutableStateOf("") }
    var users by remember { mutableStateOf(setOf<String>()) }
    if (path.isEmpty()) vols.value?.firstOrNull()?.let { path = "$it/TimeMachine" }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(stringResource(R.string.tr_tm_create)) }, text = {
        Column(androidx.compose.ui.Modifier.verticalScroll(androidx.compose.foundation.rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Field(stringResource(R.string.tr_share_name), name, { name = it.trim() })
            Field(stringResource(R.string.ctl_dest_folder), path, { path = it.trim() })
            Field(stringResource(R.string.tr_tm_limit), gb, { gb = it.filter(Char::isDigit) }, number = true)
            Text(stringResource(R.string.tr_tm_who), style = MaterialTheme.typography.labelLarge)
            who.value.orEmpty().forEach { u ->
                Row(Modifier.fillMaxWidth().clickable { users = if (u in users) users - u else users + u }, verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(u in users, { users = if (it) users + u else users - u }); Text(u)
                }
            }
        }
    }, confirmButton = {
        TextButton(enabled = name.isNotEmpty() && path.isNotEmpty() && users.isNotEmpty(), onClick = { onCreate(JsonObject(emptyMap()).put("name" to name, "path" to path, "maxGb" to (gb.toIntOrNull() ?: 0), "users" to users.toList())) }) { Text(stringResource(R.string.usr_create)) }
    }, dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
}


// ---- Backup vaults ----

@Composable
fun VaultsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.jsonList("/api/vaults") }
    val actions = rememberActions()
    val r = LocalResources.current
    var ask by remember { mutableStateOf<Ask2?>(null) }
    var code by remember { mutableStateOf<String?>(null) }
    var creating by remember { mutableStateOf(false) }
    AreaScreen(stringResource(R.string.tr_vaults), onBack, actions = { IconButton(onClick = { creating = true }) { Icon(Icons.Default.Add, stringResource(R.string.tr_new_vault)) } }) { pad ->
        Refreshable(data, pad) { list ->
            ScrollColumn {
                Hint(stringResource(R.string.tr_vaults_help))
                if (list.isEmpty()) SectionCard { Hint(stringResource(R.string.tr_no_vaults)) }
                list.forEach { v ->
                    SectionCard {
                        CardHead(v.s("name"), v.s("path")) { if (v.b("appendOnly")) StatusBadge(stringResource(R.string.tr_append_only), Tone.Ok) }
                        v.objs("clients").forEach { c ->
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Column(Modifier.weight(1f)) {
                                    Text(c.s("name"), style = MaterialTheme.typography.bodyLarge)
                                    Text((if (v.b("shared")) "" else formatBytes(c.l("size")) + " · ") + (whenText(c.s("lastSeen")).ifEmpty { r.getString(R.string.ctl_never) }), style = MaterialTheme.typography.bodySmall)
                                }
                                TextButton(onClick = { ask = Ask2(r.getString(R.string.tr_remove_access, c.s("name")), r.getString(R.string.tr_remove_access_text), r.getString(R.string.ph_remove_access)) { actions.run(after = data.reload) { it.sendObj("DELETE", "/api/vaults/${v.s("id")}/clients/${c.s("id")}") } } }) {
                                    Text(stringResource(R.string.ph_remove_access), color = MaterialTheme.colorScheme.error)
                                }
                            }
                        }
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(enabled = !actions.busy, onClick = { actions.run { a -> code = a.sendObj("POST", "/api/vaults/${v.s("id")}/code").o().s("code") } }) { Text(stringResource(R.string.tr_pairing_code)) }
                            TextButton(onClick = { ask = Ask2(r.getString(R.string.tr_delete_vault, v.s("name")), r.getString(R.string.tr_delete_vault_text), r.getString(R.string.b_delete)) { actions.run(after = data.reload) { it.sendObj("DELETE", "/api/vaults/${v.s("id")}") } } }) {
                                Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error)
                            }
                        }
                    }
                }
            }
        }
    }
    ask?.let { a -> Confirm(a.title, a.text, a.confirm, onConfirm = a.run, onDismiss = { ask = null }) }
    code?.let { c -> AlertDialog(onDismissRequest = { code = null }, title = { Text(stringResource(R.string.tr_pairing_code)) }, text = {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            androidx.compose.foundation.text.selection.SelectionContainer { Text(c, style = MaterialTheme.typography.headlineSmall, fontFamily = androidx.compose.ui.text.font.FontFamily.Monospace) }
            Hint(stringResource(R.string.tr_pairing_help))
        }
    }, confirmButton = { TextButton(onClick = { code = null }) { Text(stringResource(R.string.b_ok)) } }) }
    if (creating) {
        var name by remember { mutableStateOf("") }
        var path by remember { mutableStateOf("") }
        var append by remember { mutableStateOf(true) }
        var shared by remember { mutableStateOf(false) }
        AlertDialog(onDismissRequest = { creating = false }, title = { Text(stringResource(R.string.tr_new_vault)) }, text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Field(stringResource(R.string.name), name, { name = it })
                Field(stringResource(R.string.ctl_dest_folder), path, { path = it.trim() })
                SwitchLine(stringResource(R.string.tr_append_only), append, sub = stringResource(R.string.tr_append_only_help)) { append = it }
                SwitchLine(stringResource(R.string.tr_shared_repo), shared) { shared = it }
            }
        }, confirmButton = {
            TextButton(enabled = name.isNotBlank() && path.isNotEmpty(), onClick = {
                creating = false
                actions.run(done = r.getString(R.string.tr_vault_created), after = data.reload) { it.sendObj("POST", "/api/vaults", JsonObject(emptyMap()).put("name" to name.trim(), "path" to path, "appendOnly" to append, "shared" to shared)) }
            }) { Text(stringResource(R.string.usr_create)) }
        }, dismissButton = { TextButton(onClick = { creating = false }) { Text(stringResource(R.string.b_cancel)) } })
    }
}
