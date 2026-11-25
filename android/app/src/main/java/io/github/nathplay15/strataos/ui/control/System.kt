package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.NotificationsNone
import androidx.compose.material.icons.filled.Article
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.NasJob as Job
import io.github.nathplay15.strataos.net.manage.a
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.certificates
import io.github.nathplay15.strataos.net.manage.clearNotifications
import io.github.nathplay15.strataos.net.manage.d
import io.github.nathplay15.strataos.net.manage.distCheck
import io.github.nathplay15.strataos.net.manage.distDismiss
import io.github.nathplay15.strataos.net.manage.distReboot
import io.github.nathplay15.strataos.net.manage.distShow
import io.github.nathplay15.strataos.net.manage.distStart
import io.github.nathplay15.strataos.net.manage.distUnplan
import io.github.nathplay15.strataos.net.manage.distUpgrade
import io.github.nathplay15.strataos.net.manage.energy
import io.github.nathplay15.strataos.net.manage.hardware
import io.github.nathplay15.strataos.net.manage.journal
import io.github.nathplay15.strataos.net.manage.json
import io.github.nathplay15.strataos.net.manage.l
import io.github.nathplay15.strataos.net.manage.nasEvents
import io.github.nathplay15.strataos.net.manage.notifications
import io.github.nathplay15.strataos.net.manage.o
import io.github.nathplay15.strataos.net.manage.objs
import io.github.nathplay15.strataos.net.manage.readNotifications
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.net.manage.setHostname
import io.github.nathplay15.strataos.net.manage.setTimezone
import io.github.nathplay15.strataos.net.manage.standby
import io.github.nathplay15.strataos.net.manage.timezones
import io.github.nathplay15.strataos.net.manage.ups
import io.github.nathplay15.strataos.net.manage.wake
import io.github.nathplay15.strataos.ui.LocalOpenWeb
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
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.readable
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import kotlinx.serialization.json.JsonPrimitive

// System pages of the web Control Panel: the notification centre, the
// system upgrade, energy and UPS, hardware, name and time, logs and
// certificates.

// ---- Notification centre ----

@Composable
fun InboxScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 15000) { it.notifications() }
    val actions = rememberActions()
    AreaScreen(stringResource(R.string.notifications), onBack, actions = {
        TextButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.readNotifications(0) } }) { Text(stringResource(R.string.ctl_mark_read)) }
        TextButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.clearNotifications() } }) { Text(stringResource(R.string.ctl_clear)) }
    }) { pad ->
        Refreshable(data, pad) { n ->
            val items = n.objs("items")
            val read = n.l("read")
            if (items.isEmpty()) Empty(Icons.Default.NotificationsNone, stringResource(R.string.ctl_no_notifications))
            else LazyColumn(Modifier.fillMaxSize(), contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                items(items, key = { it.l("id") }) { e ->
                    Box(Modifier.readable()) {
                        SectionCard {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                if (e.l("id") > read) Box(Modifier.size(8.dp).background(MaterialTheme.colorScheme.primary, CircleShape))
                                StatusBadge(levelText(LocalResources.current, e.s("level")), toneOf(e.s("level")))
                                Text(whenText(e.s("time")), Modifier.weight(1f), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                            Text(e.s("subject"), style = MaterialTheme.typography.titleSmall)
                            if (e.s("message").isNotEmpty()) Text(e.s("message"), style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                }
            }
        }
    }
}

fun levelText(r: android.content.res.Resources, level: String): String = r.getString(when (level) {
    "error" -> R.string.nas_level_error
    "warning" -> R.string.nas_level_warning
    else -> R.string.ctl_level_info
})

// ---- System upgrade (Debian) ----

private val finished = setOf("done", "failed", "rolled-back")

@Composable
fun DistUpgradeScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 15000) { it.distUpgrade() }
    val actions = rememberActions()
    val r = LocalResources.current
    val web = LocalOpenWeb.current
    var job by remember { mutableStateOf<Job?>(null) }
    var start by remember { mutableStateOf(false) }
    var reboot by remember { mutableStateOf(false) }
    AreaScreen(stringResource(R.string.ctl_dist), onBack) { pad ->
        Refreshable(data, pad) { d ->
            val run = d["run"]?.let { it as? kotlinx.serialization.json.JsonObject }
            val busy = run != null && run.s("phase") !in finished
            val chk = d["check"].o().takeIf { it.s("target") == d.s("target") && d.s("target").isNotEmpty() }
            val n = d.s("targetVersion")
            ScrollColumn {
                SectionCard {
                    CardHead(stringResource(R.string.ctl_dist_current, d.s("currentVersion"), d.s("current")), d.s("reason").takeIf { !d.b("available") })
                    if (d.s("target").isNotEmpty() && !busy) OutlinedButton(enabled = !actions.busy, onClick = { actions.run { job = it.distCheck() } }) { Text(stringResource(R.string.ctl_check_again)) }
                    if (chk != null) Hint(stringResource(R.string.ctl_last_check, whenText(chk.s("at"))))
                    else if (d.s("target").isNotEmpty() && !busy) Hint(stringResource(R.string.ctl_not_checked))
                }
                if (d.b("proposal") && !busy) SectionCard(stringResource(R.string.ctl_dist_propose, n)) {
                    Text(stringResource(R.string.ctl_dist_propose_text, n, d.s("target")), style = MaterialTheme.typography.bodyMedium)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        Button(enabled = !actions.busy, onClick = { start = true }) { Text(stringResource(R.string.ctl_yes)) }
                        OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.distDismiss() } }) { Text(stringResource(R.string.ctl_no)) }
                    }
                }
                if (d.b("dismissed") && !busy) SectionCard {
                    Text(stringResource(R.string.ctl_dist_hidden, n), style = MaterialTheme.typography.bodyMedium)
                    TextButton(onClick = { actions.run(after = data.reload) { it.distShow() } }) { Text(stringResource(R.string.ctl_show_again)) }
                }
                d["scheduled"]?.let { it as? kotlinx.serialization.json.JsonObject }?.let {
                    SectionCard {
                        Text(stringResource(R.string.ctl_dist_scheduled, n, whenText(d.s("nextWindow")).ifEmpty { "—" }), style = MaterialTheme.typography.bodyMedium)
                        TextButton(onClick = { actions.run(after = data.reload) { a -> a.distUnplan() } }) { Text(stringResource(R.string.b_cancel)) }
                    }
                }
                if (d.b("available") && chk?.b("blocked") == true && !busy) SectionCard(stringResource(R.string.ctl_dist_not_ready, n)) {
                    chk.objs("checks").filter { it.s("level") == "block" }.forEach { c -> CheckLine(c) }
                    Hint(stringResource(R.string.ctl_dist_force_web))
                    TextButton(onClick = { web("/system/updates", null) }) { Text(stringResource(R.string.area_web)) }
                }
                if (run != null) SectionCard {
                    CardHead(stringResource(R.string.ctl_dist_run, run.s("fromNumber").ifEmpty { run.s("from") }, run.s("toNumber").ifEmpty { run.s("to") }), whenText(run.s("started"))) {
                        StatusBadge(run.s("phase"), when (run.s("phase")) { "failed" -> Tone.Bad; "rolled-back" -> Tone.Warn; "done" -> Tone.Ok; else -> Tone.Accent })
                    }
                    if (run.s("restorePoint").isNotEmpty()) KeyValue(stringResource(R.string.nas_restore), run.s("restorePoint"), mono = true)
                    if (run.s("error").isNotEmpty()) Text(run.s("error"), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                    if (busy) Hint(stringResource(R.string.ctl_dist_busy))
                    if (run.s("phase") == "reboot" && !run.b("auto")) Button(onClick = { reboot = true }) { Text(stringResource(R.string.ctl_restart_now)) }
                    run.objs("postChecks").forEach { c -> CheckLine(c) }
                }
            }
            if (start) Confirm(
                stringResource(R.string.ctl_dist_upgrade_to, n), stringResource(R.string.ctl_dist_start_text), stringResource(R.string.ctl_now), destructive = false,
                onConfirm = {
                    actions.run(after = data.reload) { a ->
                        val o = a.distStart(now = true, force = false, confirm = "")
                        if (o.containsKey("id") && o.containsKey("status")) job = kotlinx.serialization.json.Json { ignoreUnknownKeys = true }.decodeFromJsonElement(Job.serializer(), o)
                    }
                },
                onDismiss = { start = false },
                extra = {
                    TextButton(onClick = { start = false; actions.run(done = r.getString(R.string.ctl_dist_planned), after = data.reload) { a -> a.distStart(now = false, force = false, confirm = "") } }) {
                        Text(stringResource(R.string.ctl_in_window))
                    }
                },
            )
        }
    }
    if (reboot) Confirm(stringResource(R.string.nas_reboot), stringResource(R.string.ctl_dist_reboot_text), stringResource(R.string.nas_reboot),
        onConfirm = { actions.run(done = r.getString(R.string.nas_rebooting)) { it.distReboot() } }, onDismiss = { reboot = false })
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

@Composable
private fun CheckLine(c: kotlinx.serialization.json.JsonObject) {
    Column {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            StatusBadge(c.s("level"), toneOf(c.s("level")))
            Text(c.s("text"), style = MaterialTheme.typography.bodyMedium)
        }
        if (c.s("fix").isNotEmpty() && c.s("level") != "ok") Text(c.s("fix"), Modifier.padding(start = 8.dp), style = MaterialTheme.typography.bodySmall)
    }
}

// ---- Energy and UPS ----

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun EnergyScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 10000) { it.energy() to runCatching { it.ups() }.getOrNull() }
    val actions = rememberActions()
    val r = LocalResources.current
    val web = LocalOpenWeb.current
    AreaScreen(stringResource(R.string.ctl_energy), onBack, actions = {
        TextButton(onClick = { web("/system/energy", null) }) { Text(stringResource(R.string.ctl_settings_web)) }
    }) { pad ->
        Refreshable(data, pad) { (e, u) ->
            ScrollColumn {
                SectionCard(stringResource(R.string.nas_power)) {
                    e.d("watts")?.let { KeyValue(stringResource(R.string.ctl_consumption), "%.0f W".format(it)) }
                    if (e.s("nextShutdown").isNotEmpty()) KeyValue(stringResource(R.string.ctl_next_shutdown), whenText(e.s("nextShutdown")))
                    if (e.s("nextWake").isNotEmpty()) KeyValue(stringResource(R.string.ctl_next_wake), whenText(e.s("nextWake")))
                    e["nextAction"].o().takeIf { it.s("time").isNotEmpty() }?.let { KeyValue(stringResource(R.string.ctl_next_action), it.s("action") + " · " + whenText(it.s("time"))) }
                    KeyValue(stringResource(R.string.ctl_governor), e.s("governor"))
                }
                val disks = e.objs("disks")
                if (disks.isNotEmpty()) SectionCard(stringResource(R.string.a_disks)) {
                    disks.forEach { dk ->
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(dk.s("path") + " · " + formatBytes(dk.l("size")), style = MaterialTheme.typography.bodyLarge)
                                Text(dk.s("model"), style = MaterialTheme.typography.bodySmall)
                            }
                            StatusBadge(dk.s("state"), if (dk.s("state") == "active/idle") Tone.Ok else Tone.Neutral)
                        }
                    }
                    OutlinedButton(enabled = !actions.busy && e.b("hdparm"), onClick = {
                        actions.run(after = data.reload) { a -> val w = a.standby(); actions.say((w as? kotlinx.serialization.json.JsonObject)?.s("warning")?.ifEmpty { null } ?: r.getString(R.string.ctl_spun_down)) }
                    }) { Text(stringResource(R.string.ctl_spin_down)) }
                }
                val devices = e["config"].o().objs("devices")
                if (devices.isNotEmpty()) SectionCard(stringResource(R.string.ctl_wol)) {
                    devices.forEach { dv ->
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(dv.s("name"), style = MaterialTheme.typography.bodyLarge)
                                Text(dv.s("mac"), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                            }
                            TextButton(enabled = !actions.busy, onClick = { actions.run(done = r.getString(R.string.ctl_wake_sent)) { it.wake(dv.s("mac")) } }) { Text(stringResource(R.string.ctl_wake)) }
                        }
                    }
                }
                SectionCard(stringResource(R.string.ctl_ups)) {
                    val st = u?.get("status").o()
                    when {
                        u == null || !u.b("installed") -> Text(stringResource(R.string.nas_not_installed), style = MaterialTheme.typography.bodyMedium)
                        u["config"].o().s("mode") == "off" -> Text(stringResource(R.string.ctl_ups_off), style = MaterialTheme.typography.bodyMedium)
                        else -> {
                            CardHead(st.s("model").ifEmpty { stringResource(R.string.ctl_ups) }) { StatusBadge(st.s("status").ifEmpty { "?" }, if (st.s("status").startsWith("OL")) Tone.Ok else Tone.Warn) }
                            st.d("charge")?.let { KeyValue(stringResource(R.string.ctl_charge), "%.0f %%".format(it)) }
                            st.d("runtime")?.let { KeyValue(stringResource(R.string.ctl_runtime), "%d min".format((it / 60).toInt())) }
                            st.d("load")?.let { KeyValue(stringResource(R.string.ctl_load), "%.0f %%".format(it)) }
                            if (u.s("error").isNotEmpty()) Text(u.s("error"), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                        }
                    }
                    TextButton(onClick = { web("/system/ups", null) }) { Text(stringResource(R.string.ctl_settings_web)) }
                }
            }
        }
    }
}

// ---- Hardware ----

@Composable
fun HardwareScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 5000) { it.hardware() }
    val web = LocalOpenWeb.current
    AreaScreen(stringResource(R.string.ctl_hardware), onBack, actions = {
        TextButton(onClick = { web("/system/hardware", null) }) { Text(stringResource(R.string.ctl_settings_web)) }
    }) { pad ->
        Refreshable(data, pad) { h ->
            val dmi = h["dmi"].o()
            ScrollColumn {
                SectionCard(stringResource(R.string.ctl_machine)) {
                    KeyValue(stringResource(R.string.ctl_model), listOf(dmi.s("vendor"), dmi.s("product")).filter { it.isNotEmpty() }.joinToString(" "))
                    if (dmi.s("board").isNotEmpty()) KeyValue(stringResource(R.string.ctl_board), dmi.s("board"))
                    if (dmi.s("cpu").isNotEmpty()) KeyValue("CPU", dmi.s("cpu"))
                }
                SectionCard(stringResource(R.string.ctl_temperatures)) {
                    h.d("cpuTemp")?.let { KeyValue("CPU", "%.0f °C".format(it)) }
                    h.objs("diskTemps").forEach { KeyValue(it.s("device"), "%d °C".format(it.l("temp"))) }
                }
                val fans = h.objs("sensors").filter { it.l("rpm") > 0 }
                SectionCard(stringResource(R.string.ctl_fans)) {
                    KeyValue(stringResource(R.string.ctl_fan_profile), h.s("profile"))
                    fans.forEach { KeyValue(it.s("label").ifEmpty { it.s("chip") }, "${it.l("rpm")} rpm") }
                    if (h.s("error").isNotEmpty()) Text(h.s("error"), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
}

// ---- Name and time ----

@Composable
fun IdentityScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { a -> a.json("/api/system/info").o() to a.timezones() }
    val actions = rememberActions()
    val r = LocalResources.current
    var askHost by remember { mutableStateOf(false) }
    AreaScreen(stringResource(R.string.ctl_identity), onBack) { pad ->
        Refreshable(data, pad) { (info, zones) ->
            var host by rememberSaveable(info.s("hostname")) { mutableStateOf(info.s("hostname")) }
            var zone by rememberSaveable(info.s("timezone")) { mutableStateOf(info.s("timezone")) }
            val hostOk = Regex("^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$").matches(host)
            ScrollColumn {
                SectionCard(stringResource(R.string.nas_hostname)) {
                    Field(stringResource(R.string.nas_hostname), host, { host = it.trim() }, error = if (!hostOk && host.isNotEmpty()) stringResource(R.string.ctl_hostname_rule) else null)
                    Hint(stringResource(R.string.ctl_hostname_hint))
                    Button(enabled = hostOk && host != info.s("hostname") && !actions.busy, onClick = { askHost = true }) { Text(stringResource(R.string.b_save)) }
                }
                SectionCard(stringResource(R.string.ctl_timezone)) {
                    Choice(stringResource(R.string.ctl_timezone), zones.map { it to it }, zone, { zone = it })
                    Button(enabled = zone.isNotEmpty() && zone != info.s("timezone") && !actions.busy, onClick = {
                        actions.run(done = r.getString(R.string.ctl_timezone_done), after = data.reload) { it.setTimezone(zone) }
                    }) { Text(stringResource(R.string.b_save)) }
                }
            }
            if (askHost) Confirm(stringResource(R.string.ctl_rename_nas), stringResource(R.string.ctl_rename_nas_text, host), stringResource(R.string.b_save), destructive = false,
                onConfirm = { actions.run(done = r.getString(R.string.ctl_hostname_done), after = data.reload) { it.setHostname(host) } }, onDismiss = { askHost = false })
        }
    }
}

// ---- Logs ----

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun LogsScreen(onBack: () -> Unit) {
    var events by rememberSaveable { mutableStateOf(true) }
    var priority by rememberSaveable { mutableStateOf("6") }
    var grep by rememberSaveable { mutableStateOf("") }
    var search by remember { mutableStateOf("") }
    LaunchedEffect(grep) { kotlinx.coroutines.delay(500); search = grep }
    val data = rememberData(events, priority, search, every = 10000) { a ->
        if (events) a.nasEvents().filter { search.isEmpty() || it.s("message").contains(search, true) || it.s("category").contains(search, true) }
        else a.journal("", priority, search)
    }
    AreaScreen(stringResource(R.string.ctl_logs), onBack) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            Column(Modifier.readable().padding(horizontal = 16.dp)) {
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilterChip(events, { events = true }, label = { Text(stringResource(R.string.ctl_events)) })
                    FilterChip(!events, { events = false }, label = { Text(stringResource(R.string.ctl_journal)) })
                    if (!events) listOf("3" to R.string.nas_level_error, "4" to R.string.nas_level_warning, "6" to R.string.ctl_level_info).forEach { (p, l) ->
                        FilterChip(priority == p, { priority = p }, label = { Text(stringResource(l)) })
                    }
                }
                Field(stringResource(R.string.a_search), grep, { grep = it })
            }
            Refreshable(data, androidx.compose.foundation.layout.PaddingValues(0.dp)) { list ->
                if (list.isEmpty()) Empty(Icons.Default.Article, stringResource(R.string.ctl_no_logs))
                else LazyColumn(Modifier.fillMaxSize(), contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp, 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    items(list) { e ->
                        Column(Modifier.readable()) {
                            if (events) {
                                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                    StatusBadge(e.s("category"), toneOf(e.s("level")))
                                    Text(whenText(e.s("time")) + (e.s("user").takeIf { it.isNotEmpty() }?.let { " · $it" } ?: ""), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                                Text(e.s("message"), style = MaterialTheme.typography.bodyMedium)
                            } else {
                                Text(whenText(e.s("time")) + " · " + e.s("ident").ifEmpty { e.s("unit") }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                Text(e.s("message"), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                                    color = when { e.l("priority") <= 3 -> MaterialTheme.colorScheme.error; e.l("priority") == 4L -> androidx.compose.ui.graphics.Color(0xFFB98500); else -> MaterialTheme.colorScheme.onSurface })
                            }
                        }
                    }
                }
            }
        }
    }
}

// ---- Certificates ----

@Composable
fun CertificatesScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.certificates() }
    val web = LocalOpenWeb.current
    AreaScreen(stringResource(R.string.ctl_certs), onBack, actions = {
        TextButton(onClick = { web("/system/settings", null) }) { Text(stringResource(R.string.ctl_settings_web)) }
    }) { pad ->
        Refreshable(data, pad) { c ->
            ScrollColumn {
                c.objs("certificates").forEach { ct ->
                    SectionCard {
                        CardHead(ct.s("subject"), ct.s("source")) { if (ct.b("active")) StatusBadge(stringResource(R.string.ctl_in_use), Tone.Ok) }
                        KeyValue(stringResource(R.string.ctl_issuer), ct.s("issuer"))
                        val names = ct["dnsNames"].a().joinToString(", ") { (it as? JsonPrimitive)?.content.orEmpty() }
                        if (names.isNotEmpty()) KeyValue(stringResource(R.string.ctl_names), names)
                        KeyValue(stringResource(R.string.ctl_expires), whenText(ct.s("notAfter")))
                    }
                }
                Hint(stringResource(R.string.ctl_certs_hint))
            }
        }
    }
}
