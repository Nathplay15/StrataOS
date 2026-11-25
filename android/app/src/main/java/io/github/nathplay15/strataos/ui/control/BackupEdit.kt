package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
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
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

// Creating and editing backup tasks, the restore wizard, UPS and address
// blocking settings: the forms of the web pages (same routes, same server
// checks and messages). An edited task is the object the NAS gave with the
// form's fields replaced, like the web's {...task, ...form}.

internal fun JsonObject.put(vararg kv: Pair<String, Any?>): JsonObject = JsonObject(this + kv.associate { (k, v) ->
    k to when (v) {
        null -> kotlinx.serialization.json.JsonNull
        is JsonElement -> v
        is Boolean -> JsonPrimitive(v)
        is Number -> JsonPrimitive(v)
        is List<*> -> JsonArray(v.map { JsonPrimitive(it.toString()) })
        else -> JsonPrimitive(v.toString())
    }
})

internal fun strings(e: JsonElement?): List<String> = e.a().mapNotNull { (it as? JsonPrimitive)?.content }

/** Schedules offered as chips (the cron field stays editable, like the web's custom plan). */
private val cronPresets = listOf("0 1 * * *", "0 * * * *", "0 1 * * 0", "")

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun BackupEditor(initial: JsonObject?, onDone: () -> Unit) {
    val actions = rememberActions()
    val r = LocalResources.current
    val sources = rememberData(Unit) { it.json("/api/backups/sources").o() }
    val base = initial ?: JsonObject(emptyMap()).put("kind" to "rsync", "destType" to "local", "port" to 22, "enabled" to true, "keepLast" to 7, "keepDaily" to 7, "keepWeekly" to 4, "keepMonthly" to 12)
    var name by remember { mutableStateOf(base.s("name")) }
    var kind by remember { mutableStateOf(base.s("kind").ifEmpty { "rsync" }) }
    var picked by remember { mutableStateOf(strings(base["sources"]).toSet()) }
    var destType by remember { mutableStateOf(base.s("destType").ifEmpty { "local" }) }
    var destPath by remember { mutableStateOf(base.s("destPath")) }
    var host by remember { mutableStateOf(base.s("host")) }
    var user by remember { mutableStateOf(base.s("user")) }
    var port by remember { mutableStateOf(base.l("port").takeIf { it > 0 }?.toString() ?: "22") }
    var endpoint by remember { mutableStateOf(base.s("s3Endpoint")) }
    var accessKey by remember { mutableStateOf(base.s("accessKey")) }
    var secretKey by remember { mutableStateOf(base.s("secretKey")) }
    var password by remember { mutableStateOf(base.s("password")) }
    var keepLast by remember { mutableStateOf(base.l("keepLast").toString()) }
    var keepDays by remember { mutableStateOf(base.l("keepDays").toString()) }
    var versions by remember { mutableStateOf(base.b("versions")) }
    var delete by remember { mutableStateOf(base.b("delete")) }
    var cron by remember { mutableStateOf(if (initial == null) "0 1 * * *" else base.s("cron")) }
    var excludes by remember { mutableStateOf(strings(base["excludes"]).joinToString("\n")) }
    // Kinds the form does not cover (pull from another server, ZFS replication, sync, repositories, vaults) keep their own fields.
    val simple = kind in setOf("rsync", "restic") && base.s("sourceType").isEmpty() && destType !in setOf("datastore", "strataos")
    fun payload(): JsonObject {
        var o = base.put("name" to name.trim(), "cron" to cron.trim(), "excludes" to excludes.lines().map { it.trim() }.filter { it.isNotEmpty() })
        if (simple) o = o.put(
            "kind" to kind, "sources" to picked.toList(), "destType" to destType, "destPath" to destPath.trim(), "host" to host.trim(), "user" to user.trim(),
            "port" to (port.toIntOrNull() ?: 22), "s3Endpoint" to endpoint.trim(), "accessKey" to accessKey.trim(), "secretKey" to secretKey, "password" to password,
            "keepLast" to (keepLast.toIntOrNull() ?: 0), "keepDays" to (keepDays.toIntOrNull() ?: 0), "versions" to versions, "delete" to delete,
        )
        return o
    }
    AreaScreen(if (initial == null) stringResource(R.string.ctl_new_backup) else r.getString(R.string.ctl_edit_title, base.s("name")), onDone) { pad ->
        Refreshable(sources, pad) { src ->
            ScrollColumn {
                SectionCard {
                    Field(stringResource(R.string.name), name, { name = it })
                    if (initial == null) FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        FilterChip(kind == "rsync", { kind = "rsync"; destType = "local" }, label = { Text(stringResource(R.string.ctl_kind_copy)) })
                        FilterChip(kind == "restic", { kind = "restic"; destType = "local" }, label = { Text(stringResource(R.string.ctl_kind_restic)) })
                    }
                    Hint(stringResource(if (kind == "restic") R.string.ctl_kind_restic_help else R.string.ctl_kind_copy_help))
                }
                if (simple) {
                    SectionCard(stringResource(R.string.ctl_sources)) {
                        src.objs("folders").forEach { f ->
                            val p = f.s("path")
                            Row(Modifier.fillMaxWidth().clickable { picked = if (p in picked) picked - p else picked + p }, verticalAlignment = Alignment.CenterVertically) {
                                Checkbox(p in picked, { picked = if (it) picked + p else picked - p })
                                Column { Text(p, style = MaterialTheme.typography.bodyMedium); Text(f.s("label"), style = MaterialTheme.typography.bodySmall) }
                            }
                        }
                    }
                    SectionCard(stringResource(R.string.ctl_destination)) {
                        val types = if (kind == "restic") listOf("local" to r.getString(R.string.ctl_dest_local), "sftp" to "SFTP", "s3" to "S3 / B2 / MinIO", "rest" to "restic REST")
                        else listOf("local" to r.getString(R.string.ctl_dest_local), "ssh" to r.getString(R.string.ctl_dest_ssh))
                        Choice(stringResource(R.string.ctl_destination), types, destType, { destType = it })
                        if (destType in setOf("ssh", "sftp")) {
                            Field(stringResource(R.string.ctl_host), host, { host = it.trim() })
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Field(stringResource(R.string.usr_username), user, { user = it.trim() }, Modifier.weight(2f))
                                Field(stringResource(R.string.ctl_port), port, { port = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
                            }
                            Hint(stringResource(R.string.ctl_ssh_key_hint))
                        }
                        if (destType == "s3") {
                            Field(stringResource(R.string.ctl_endpoint), endpoint, { endpoint = it.trim() })
                            Field(stringResource(R.string.ctl_access_key), accessKey, { accessKey = it.trim() })
                            Field(stringResource(R.string.ctl_secret_key), secretKey, { secretKey = it }, secret = true)
                        }
                        Field(stringResource(if (destType == "s3") R.string.ctl_bucket_path else if (destType == "rest") R.string.ctl_rest_url else R.string.ctl_dest_folder), destPath, { destPath = it.trim() },
                            help = if (destType == "local") src.objs("folders").filter { it.s("label").startsWith("Volume") || it.s("label").startsWith("ZFS") }.joinToString("  ") { it.s("path") } else null)
                        if (kind == "restic") Field(stringResource(R.string.ctl_repo_password), password, { password = it }, secret = true, help = stringResource(R.string.ctl_repo_password_help))
                    }
                    SectionCard(stringResource(R.string.ctl_retention)) {
                        if (kind == "restic") {
                            Field(stringResource(R.string.ctl_keep_last), keepLast, { keepLast = it.filter(Char::isDigit) }, number = true)
                        } else {
                            SwitchLine(stringResource(R.string.ctl_versions_switch), versions) { versions = it }
                            if (versions) Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Field(stringResource(R.string.ctl_keep_last), keepLast, { keepLast = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
                                Field(stringResource(R.string.ctl_keep_days), keepDays, { keepDays = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
                            }
                            else SwitchLine(stringResource(R.string.ctl_mirror_delete), delete) { delete = it }
                        }
                    }
                } else SectionCard { Hint(stringResource(R.string.ctl_backup_kind_web)) }
                SectionCard(stringResource(R.string.ctl_when)) {
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        listOf(R.string.ctl_cron_daily, R.string.ctl_cron_hourly, R.string.ctl_cron_weekly, R.string.ctl_on_demand).forEachIndexed { i, l ->
                            FilterChip(cron == cronPresets[i], { cron = cronPresets[i] }, label = { Text(stringResource(l)) })
                        }
                    }
                    Field(stringResource(R.string.ctl_cron), cron, { cron = it }, help = stringResource(R.string.ctl_cron_help))
                    CronPreview(cron)
                }
                SectionCard(stringResource(R.string.ctl_excludes)) {
                    Field(stringResource(R.string.ctl_excludes), excludes, { excludes = it }, singleLine = false, help = stringResource(R.string.ctl_excludes_help))
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(enabled = !actions.busy, onClick = {
                        actions.run { a -> actions.say(a.sendObj("POST", "/api/backups/test", payload()).o().s("message").ifEmpty { r.getString(R.string.ctl_test_ok) }) }
                    }) { Text(stringResource(R.string.ctl_test_dest)) }
                    Button(enabled = !actions.busy && name.isNotBlank() && (!simple || (picked.isNotEmpty() && destPath.isNotBlank())), onClick = {
                        actions.run(done = r.getString(if (initial == null) R.string.ctl_backup_created else R.string.ctl_backup_updated), after = onDone) { a ->
                            if (initial == null) a.sendObj("POST", "/api/backups", payload()) else a.sendObj("PUT", "/api/backups/${initial.s("id")}", payload())
                        }
                    }) { Text(stringResource(R.string.b_save)) }
                }
            }
        }
    }
}

/** The next runs of a cron expression, worked out by the NAS (or its error). */
@Composable
private fun CronPreview(cron: String) {
    var text by remember { mutableStateOf("") }
    val ctx = androidx.compose.ui.platform.LocalContext.current
    LaunchedEffect(cron) {
        if (cron.isBlank()) { text = ""; return@LaunchedEffect }
        kotlinx.coroutines.delay(400)
        text = try {
            kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                io.github.nathplay15.strataos.ui.adminkit.api().json("/api/schedules/preview", "cron" to cron).a().take(3).joinToString(" · ") { whenText((it as JsonPrimitive).content) }
            }
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            io.github.nathplay15.strataos.ui.errorText(ctx, e)
        }
    }
    if (text.isNotEmpty()) Hint(text)
}

// ---- Restore wizard ----

/** Source → point in time → content → destination, preview, then the restore job. */
@Composable
fun RestoreScreen(onBack: () -> Unit, initialSource: String = "") {
    val actions = rememberActions()
    val r = LocalResources.current
    var source by remember { mutableStateOf(initialSource) }
    var point by remember { mutableStateOf<JsonObject?>(null) }
    var dir by remember { mutableStateOf("") }
    var sel by remember { mutableStateOf(setOf<String>()) }
    var mode by remember { mutableStateOf("inplace") }
    var target by remember { mutableStateOf("") }
    var policy by remember { mutableStateOf("rename") }
    var preview by remember { mutableStateOf<JsonObject?>(null) }
    var job by remember { mutableStateOf<NasJob?>(null) }
    var confirm by remember { mutableStateOf(false) }
    fun body() = JsonObject(emptyMap()).put("source" to source, "point" to point?.s("id"), "paths" to sel.toList(), "mode" to mode, "target" to (if (mode == "folder") target.trim() else ""), "policy" to policy)
    val back: () -> Unit = {
        when {
            preview != null -> preview = null
            point != null && dir.isNotEmpty() && point!!.let { p -> strings(p["roots"]).none { it == dir } } -> dir = dir.substringBeforeLast('/')
            point != null && dir.isNotEmpty() -> dir = ""
            point != null -> { point = null; sel = emptySet() }
            source.isNotEmpty() && initialSource.isEmpty() -> source = ""
            else -> onBack()
        }
    }
    AreaScreen(stringResource(R.string.ctl_restore_title), back, subtitle = point?.let { whenText(it.s("time")) } ?: source.ifEmpty { null }) { pad ->
        when {
            source.isEmpty() -> {
                val data = rememberData(Unit) { it.jsonList("/api/restore/sources") }
                Refreshable(data, pad) { list ->
                    ScrollColumn {
                        Hint(stringResource(R.string.ctl_restore_pick_source))
                        list.forEach { s ->
                            SectionCard(modifier = Modifier.clickable { source = s.s("id") }) { CardHead(s.s("name"), s.s("detail")) { io.github.nathplay15.strataos.ui.adminkit.StatusBadge(s.s("kind"), io.github.nathplay15.strataos.ui.adminkit.Tone.Neutral) } }
                        }
                        if (list.isEmpty()) Hint(stringResource(R.string.ctl_restore_no_source))
                    }
                }
            }
            point == null -> {
                val data = rememberData(source) { it.jsonList("/api/restore/points", "source" to source) }
                Refreshable(data, pad) { list ->
                    ScrollColumn {
                        Hint(stringResource(R.string.ctl_restore_pick_point))
                        list.sortedByDescending { it.s("time") }.forEach { p ->
                            SectionCard(modifier = Modifier.clickable { point = p; dir = ""; sel = emptySet() }) {
                                CardHead(whenText(p.s("time")), listOfNotNull(p.s("label").ifEmpty { null }, p.l("files").takeIf { it > 0 }?.let { r.getString(R.string.ctl_n_files, it.toInt()) }, p.l("bytes").takeIf { it > 0 }?.let { formatBytes(it) }).joinToString(" · "))
                            }
                        }
                        if (list.isEmpty()) Hint(stringResource(R.string.ctl_no_versions))
                    }
                }
            }
            preview == null -> {
                val p = point!!
                val data = rememberData(source, p.s("id"), dir) { it.json("/api/restore/browse", "source" to source, "point" to p.s("id"), "path" to dir).o().objs("entries") }
                Column(Modifier.padding(pad)) {
                    Refreshable(data, androidx.compose.foundation.layout.PaddingValues(0.dp)) { entries ->
                        ScrollColumn {
                            if (dir.isNotEmpty()) Row(Modifier.fillMaxWidth().clickable { back() }, verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.ArrowUpward, null); Text(dir, Modifier.padding(start = 8.dp), style = MaterialTheme.typography.bodySmall)
                            }
                            entries.forEach { e ->
                                val path = e.s("path")
                                Row(Modifier.fillMaxWidth().clickable { if (e.b("dir")) dir = path else sel = if (path in sel) sel - path else sel + path }, verticalAlignment = Alignment.CenterVertically) {
                                    Checkbox(path in sel, { sel = if (it) sel.filterNot { s -> s.startsWith("$path/") || path.startsWith("$s/") }.toSet() + path else sel - path })
                                    Icon(if (e.b("dir")) Icons.Default.Folder else Icons.Default.Description, null)
                                    Column(Modifier.weight(1f).padding(start = 8.dp)) {
                                        Text(e.s("name"), style = MaterialTheme.typography.bodyMedium)
                                        if (!e.b("dir")) Text(formatBytes(e.l("size")) + " · " + whenText(e.s("mtime")), style = MaterialTheme.typography.bodySmall)
                                    }
                                }
                            }
                            SectionCard(stringResource(R.string.ctl_restore_where)) {
                                Text(r.getString(R.string.ctl_n_selected, sel.size), style = MaterialTheme.typography.bodyMedium)
                                Choice(stringResource(R.string.ctl_restore_where), listOf("inplace" to r.getString(R.string.ctl_restore_inplace), "folder" to r.getString(R.string.ctl_restore_folder)), mode, { mode = it })
                                if (mode == "folder") Field(stringResource(R.string.ctl_dest_folder), target, { target = it.trim() })
                                Choice(stringResource(R.string.ctl_restore_existing), listOf("rename" to r.getString(R.string.ctl_policy_rename), "skip" to r.getString(R.string.ctl_policy_skip), "overwrite" to r.getString(R.string.ctl_policy_overwrite)), policy, { policy = it })
                                Button(enabled = sel.isNotEmpty() && (mode != "folder" || target.isNotEmpty()) && !actions.busy, onClick = {
                                    actions.run { a -> preview = a.sendObj("POST", "/api/restore/preview", body()).o() }
                                }) { Text(stringResource(R.string.ctl_preview)) }
                            }
                        }
                    }
                }
            }
            else -> {
                val pv = preview!!
                ScrollColumn {
                    SectionCard(stringResource(R.string.ctl_preview)) {
                        KeyValue(stringResource(R.string.ctl_files), "${pv.l("files")} · ${formatBytes(pv.l("bytes"))}")
                        KeyValue(stringResource(R.string.ctl_new_files), pv.l("new").toString())
                        KeyValue(stringResource(R.string.ctl_same_files), pv.l("same").toString())
                        KeyValue(stringResource(R.string.ctl_overwritten), pv.l("overwrite").toString())
                        KeyValue(stringResource(R.string.ctl_conflicts), pv.l("conflicts").toString())
                        KeyValue(stringResource(R.string.ctl_destination), pv.s("target"))
                        Button(enabled = !actions.busy, onClick = { confirm = true }) { Text(stringResource(R.string.ctl_restore)) }
                    }
                }
            }
        }
    }
    if (confirm) Confirm(stringResource(R.string.ctl_restore_title), stringResource(R.string.ctl_restore_confirm, preview?.l("overwrite") ?: 0L), stringResource(R.string.ctl_restore),
        destructive = (preview?.l("overwrite") ?: 0) > 0,
        onConfirm = { actions.run { a -> job = a.startJob("POST", "/api/restore/run", body().toString()) } }, onDismiss = { confirm = false })
    job?.let { j -> JobDialog(j) { job = null; onBack() } }
}

// ---- UPS settings ----

@Composable
fun UpsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.json("/api/ups").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    var job by remember { mutableStateOf<NasJob?>(null) }
    AreaScreen(stringResource(R.string.ctl_ups), onBack) { pad ->
        Refreshable(data, pad) { u ->
            val c = u["config"].o()
            var mode by remember(c) { mutableStateOf(c.s("mode").ifEmpty { "off" }) }
            var driver by remember(c) { mutableStateOf(c.s("driver")) }
            var port by remember(c) { mutableStateOf(c.s("port")) }
            var rHost by remember(c) { mutableStateOf(c.s("remoteHost")) }
            var rName by remember(c) { mutableStateOf(c.s("remoteName")) }
            var rUser by remember(c) { mutableStateOf(c.s("remoteUser")) }
            var rPass by remember(c) { mutableStateOf(c.s("remotePassword")) }
            var after by remember(c) { mutableStateOf(c.l("shutdownAfter").toString()) }
            var share by remember(c) { mutableStateOf(c.b("share")) }
            ScrollColumn {
                if (!u.b("installed")) SectionCard {
                    Text(stringResource(R.string.ctl_ups_install), style = MaterialTheme.typography.bodyMedium)
                    Button(enabled = !actions.busy, onClick = { actions.run { a -> job = a.startJob("POST", "/api/system/components/nut/install") } }) { Text(stringResource(R.string.nas_install)) }
                } else {
                    SectionCard(stringResource(R.string.ctl_ups)) {
                        Choice(stringResource(R.string.ctl_ups_mode), listOf("off" to r.getString(R.string.usr_off), "usb" to r.getString(R.string.ctl_ups_usb), "netclient" to r.getString(R.string.ctl_ups_net)), mode, { mode = it })
                        if (mode == "usb") {
                            OutlinedButton(enabled = !actions.busy, onClick = {
                                actions.run { a ->
                                    val found = a.jsonList("/api/ups/detect").firstOrNull()
                                    if (found == null) actions.say(r.getString(R.string.ctl_ups_none_found)) else { driver = found.s("driver"); port = found.s("port"); actions.say(found.s("vendor") + " " + found.s("product")) }
                                }
                            }) { Text(stringResource(R.string.ctl_ups_detect)) }
                            Field(stringResource(R.string.ctl_ups_driver), driver, { driver = it.trim() })
                            Field(stringResource(R.string.ctl_port), port, { port = it.trim() })
                            SwitchLine(stringResource(R.string.ctl_ups_share), share) { share = it }
                        }
                        if (mode == "netclient") {
                            Field(stringResource(R.string.ctl_host), rHost, { rHost = it.trim() })
                            Field(stringResource(R.string.ctl_ups_name), rName, { rName = it.trim() })
                            Field(stringResource(R.string.usr_username), rUser, { rUser = it.trim() })
                            Field(stringResource(R.string.usr_password), rPass, { rPass = it }, secret = true)
                        }
                        if (mode != "off") Field(stringResource(R.string.ctl_ups_after), after, { after = it.filter(Char::isDigit) }, number = true, help = stringResource(R.string.ctl_ups_after_help))
                        Button(enabled = !actions.busy, onClick = {
                            val o = c.put("mode" to mode, "driver" to driver, "port" to port, "remoteHost" to rHost, "remoteName" to rName, "remoteUser" to rUser, "remotePassword" to rPass,
                                "shutdownAfter" to (after.toIntOrNull() ?: 0), "share" to share)
                            actions.run(done = r.getString(R.string.ctl_ups_saved), after = data.reload) { a -> a.sendObj("PUT", "/api/ups", o) }
                        }) { Text(stringResource(R.string.b_save)) }
                    }
                }
            }
        }
    }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

// ---- Address blocking settings ----

@Composable
fun GuardSettingsCard(cfg: JsonObject, busy: Boolean, save: (JsonObject) -> Unit) {
    var on by remember(cfg) { mutableStateOf(cfg.b("enabled")) }
    var max by remember(cfg) { mutableStateOf(cfg.l("maxFailures").toString()) }
    var win by remember(cfg) { mutableStateOf(cfg.l("windowMin").toString()) }
    var blk by remember(cfg) { mutableStateOf(cfg.l("blockMin").toString()) }
    var lan by remember(cfg) { mutableStateOf(cfg.b("neverLan")) }
    SectionCard(stringResource(R.string.ctl_guard_settings)) {
        SwitchLine(stringResource(R.string.ctl_guard_on), on, sub = stringResource(R.string.ctl_guard_on_help)) { on = it }
        if (on) {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Field(stringResource(R.string.ctl_guard_failures), max, { max = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
                Field(stringResource(R.string.ctl_guard_window), win, { win = it.filter(Char::isDigit) }, Modifier.weight(1f), number = true)
            }
            Field(stringResource(R.string.ctl_guard_block_min), blk, { blk = it.filter(Char::isDigit) }, number = true)
            SwitchLine(stringResource(R.string.ctl_guard_never_lan), lan) { lan = it }
        }
        TextButton(enabled = !busy, onClick = {
            save(cfg.put("enabled" to on, "maxFailures" to (max.toIntOrNull() ?: 5).coerceAtLeast(1), "windowMin" to (win.toIntOrNull() ?: 10).coerceAtLeast(1),
                "blockMin" to (blk.toIntOrNull() ?: 60).coerceAtLeast(1), "neverLan" to lan))
        }) { Text(stringResource(R.string.b_save)) }
    }
}
