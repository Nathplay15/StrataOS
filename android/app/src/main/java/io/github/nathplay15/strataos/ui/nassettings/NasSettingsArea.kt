package io.github.nathplay15.strataos.ui.nassettings

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.Lan
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.PowerSettingsNew
import androidx.compose.material.icons.filled.Security
import androidx.compose.material.icons.filled.SystemUpdate
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.AutoUpdatesReq
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.NotifyChannel
import io.github.nathplay15.strataos.net.manage.autoUpdates
import io.github.nathplay15.strataos.net.manage.checkUpdates
import io.github.nathplay15.strataos.net.manage.createRestorePoint
import io.github.nathplay15.strataos.net.manage.deleteRestorePoint
import io.github.nathplay15.strataos.net.manage.fileServices
import io.github.nathplay15.strataos.net.manage.installStrataUpdate
import io.github.nathplay15.strataos.net.manage.installSystemUpdates
import io.github.nathplay15.strataos.net.manage.network
import io.github.nathplay15.strataos.net.manage.notifySettings
import io.github.nathplay15.strataos.net.manage.power
import io.github.nathplay15.strataos.net.manage.restorePoints
import io.github.nathplay15.strataos.net.manage.rollbackRestorePoint
import io.github.nathplay15.strataos.net.manage.saveAutoUpdates
import io.github.nathplay15.strataos.net.manage.saveFileServices
import io.github.nathplay15.strataos.net.manage.saveNotifySettings
import io.github.nathplay15.strataos.net.manage.serviceAction
import io.github.nathplay15.strataos.net.manage.services
import io.github.nathplay15.strataos.net.manage.setUpdateChannel
import io.github.nathplay15.strataos.net.manage.strataUpdate
import io.github.nathplay15.strataos.net.manage.testNotification
import io.github.nathplay15.strataos.net.manage.upgradable
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.readable
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatDate

// The most used settings of the NAS, natively: updates, notifications,
// network (summary), file services, security, restore points, power. The
// rest is in the Web interface area.

@Composable
fun NasSettingsArea(back: () -> Unit) {
    var route by rememberSaveable { mutableStateOf("menu") }
    val home = { route = "menu" }
    when (route) {
        "updates" -> UpdatesScreen(home)
        "notifications" -> NotificationsScreen(home)
        "network" -> NetworkScreen(home)
        "services" -> ServicesScreen(home)
        "security" -> SecurityScreen(home)
        "restore" -> RestoreScreen(home)
        "power" -> PowerScreen(home)
        else -> AreaScreen(stringResource(R.string.area_nas_settings), back) { pad ->
            val items = listOf(
                Triple("updates", R.string.nas_updates, Icons.Default.SystemUpdate),
                Triple("notifications", R.string.nas_notifications, Icons.Default.Notifications),
                Triple("network", R.string.nas_network, Icons.Default.Lan),
                Triple("services", R.string.nas_services, Icons.Default.Dns),
                Triple("security", R.string.nas_security, Icons.Default.Security),
                Triple("restore", R.string.nas_restore, Icons.Default.History),
                Triple("power", R.string.nas_power, Icons.Default.PowerSettingsNew),
            )
            LazyColumn(Modifier.padding(pad).fillMaxSize()) {
                items(items) { (id, label, icon) -> MenuRow(stringResource(label), icon) { route = id }; HorizontalDivider(Modifier.readable()) }
                item { Text(stringResource(R.string.nas_more_web), Modifier.readable().padding(16.dp), style = MaterialTheme.typography.bodySmall) }
            }
        }
    }
}

@Composable
private fun MenuRow(label: String, icon: ImageVector, open: () -> Unit) {
    ListItem(modifier = Modifier.readable().clickable(onClick = open), leadingContent = { Icon(icon, null) }, headlineContent = { Text(label) })
}

// ---- Updates ----

private class Updates(val strata: io.github.nathplay15.strataos.net.manage.StrataUpdate, val pkgs: Int, val auto: io.github.nathplay15.strataos.net.manage.AutoUpdates)

@OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
@Composable
private fun UpdatesScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { Updates(it.strataUpdate(), it.upgradable().size, it.autoUpdates()) }
    val actions = rememberActions()
    val res = LocalResources.current
    var job by remember { mutableStateOf<NasJob?>(null) }
    var confirm by remember { mutableStateOf<String?>(null) }
    AreaScreen(stringResource(R.string.nas_updates), onBack) { pad ->
        Refreshable(data, pad) { u ->
            val a = u.auto
            var strata by remember(a) { mutableStateOf(a.strata.ifEmpty { "off" }) }
            var security by remember(a) { mutableStateOf(a.security.ifEmpty { "off" }) }
            var system by remember(a) { mutableStateOf(a.system.ifEmpty { "off" }) }
            var days by remember(a) { mutableStateOf(a.days.toSet()) }
            var hour by remember(a) { mutableStateOf(a.hour.toString()) }
            var reboot by remember(a) { mutableStateOf(a.reboot) }
            ScrollColumn {
                SectionCard("StrataOS") {
                    KeyValue(stringResource(R.string.nas_running_version), u.strata.running)
                    KeyValue(stringResource(R.string.nas_available), u.strata.candidate.takeIf { it.isNotEmpty() && it != u.strata.installed } ?: stringResource(R.string.fleet_up_to_date))
                    KeyValue(stringResource(R.string.nas_system_packages), u.pkgs.toString())
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(enabled = !actions.busy, onClick = { actions.run { job = it.checkUpdates() } }) { Text(stringResource(R.string.fleet_check)) }
                        if (u.strata.candidate.isNotEmpty() && u.strata.candidate != u.strata.installed) Button(onClick = { confirm = "strata" }) { Text(stringResource(R.string.nas_install_strata)) }
                    }
                    if (u.pkgs > 0) OutlinedButton(onClick = { confirm = "system" }) { Text(stringResource(R.string.nas_install_system, u.pkgs)) }
                }
                SectionCard(stringResource(R.string.nas_channel)) {
                    Choice(stringResource(R.string.nas_channel), listOf("stable" to stringResource(R.string.nas_stable), "beta" to stringResource(R.string.nas_beta)), a.channel, { ch ->
                        if (ch != a.channel) actions.run(after = data.reload) { job = it.setUpdateChannel(ch) }
                    }, enabled = !actions.busy)
                    Text(stringResource(R.string.nas_beta_help), style = MaterialTheme.typography.bodySmall)
                }
                SectionCard(stringResource(R.string.nas_auto)) {
                    Choice("StrataOS", modes(true), strata, { strata = it })
                    Choice(stringResource(R.string.nas_security_updates), modes(false), security, { security = it })
                    Choice(stringResource(R.string.nas_other_updates), modes(true), system, { system = it })
                    Text(stringResource(R.string.nas_window), style = MaterialTheme.typography.labelLarge)
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                        val names = java.text.DateFormatSymbols.getInstance().shortWeekdays // 1 = Sunday
                        (0..6).forEach { d -> FilterChip(d in days, { days = if (d in days) days - d else days + d }, label = { Text(names[d + 1]) }) }
                    }
                    Text(stringResource(R.string.nas_every_day_hint), style = MaterialTheme.typography.bodySmall)
                    Field(stringResource(R.string.nas_hour), hour, { hour = it.filter(Char::isDigit).take(2) }, number = true)
                    SwitchLine(stringResource(R.string.nas_auto_reboot), reboot) { reboot = it }
                    if (a.nextWindow.isNotEmpty()) KeyValue(stringResource(R.string.nas_next_window), formatDate(a.nextWindow))
                    a.last?.let { KeyValue(stringResource(R.string.nas_last_run), "${formatDate(it.started)} · ${it.status}" + if (it.message.isNotEmpty()) " · ${it.message}" else "") }
                    Button(enabled = !actions.busy && (hour.toIntOrNull() ?: -1) in 0..23, onClick = {
                        actions.run(done = res.getString(R.string.b_saved), after = data.reload) { it.saveAutoUpdates(AutoUpdatesReq(strata, security, system, days.sorted(), hour.toInt(), reboot)) }
                    }) { Text(stringResource(R.string.b_save)) }
                }
            }
        }
    }
    confirm?.let { k ->
        Confirm(stringResource(if (k == "strata") R.string.nas_install_strata else R.string.nas_install_system_title),
            stringResource(if (k == "strata") R.string.nas_install_strata_text else R.string.nas_install_system_text), stringResource(R.string.nas_install), destructive = false,
            onConfirm = { actions.run { job = if (k == "strata") it.installStrataUpdate() else it.installSystemUpdates() } }, onDismiss = { confirm = null })
    }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

@Composable
private fun modes(notify: Boolean) = listOfNotNull(
    "off" to stringResource(R.string.nas_mode_off),
    ("notify" to stringResource(R.string.nas_mode_notify)).takeIf { notify },
    "install" to stringResource(R.string.nas_mode_install),
)

// ---- Notifications ----

@Composable
private fun NotificationsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.notifySettings() }
    val actions = rememberActions()
    val res = LocalResources.current
    AreaScreen(stringResource(R.string.nas_notifications), onBack) { pad ->
        Refreshable(data, pad) { s ->
            var ch by remember(s) { mutableStateOf(s.notifyChannels) }
            var quiet by remember(s) { mutableStateOf(s.quietHours) }
            var monthly by remember(s) { mutableStateOf(s.monthlyReport) }
            ScrollColumn {
                SectionCard(stringResource(R.string.nas_channels)) {
                    Text(stringResource(R.string.nas_channels_help), style = MaterialTheme.typography.bodySmall)
                    ChannelRow(stringResource(R.string.nas_ch_mobile), true, ch.mobile) { ch = ch.copy(mobile = it) }
                    ChannelRow(stringResource(R.string.nas_ch_email), s.notifyEmail.isNotEmpty() && s.smtpHost.isNotEmpty(), ch.email) { ch = ch.copy(email = it) }
                    ChannelRow("Telegram", s.telegramToken.isNotEmpty() && s.telegramChatId.isNotEmpty(), ch.telegram) { ch = ch.copy(telegram = it) }
                    ChannelRow("ntfy", s.ntfyUrl.isNotEmpty(), ch.ntfy) { ch = ch.copy(ntfy = it) }
                    ChannelRow("Webhook", s.webhookUrl.isNotEmpty(), ch.webhook) { ch = ch.copy(webhook = it) }
                }
                SectionCard(stringResource(R.string.nas_quiet_hours)) {
                    SwitchLine(stringResource(R.string.nas_quiet_on), quiet.enabled, sub = stringResource(R.string.nas_quiet_help)) { quiet = quiet.copy(enabled = it) }
                    if (quiet.enabled) {
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            Field(stringResource(R.string.nas_from), quiet.start, { quiet = quiet.copy(start = it.take(5)) }, Modifier.weight(1f), error = if (!hhmm(quiet.start)) "HH:MM" else null)
                            Field(stringResource(R.string.nas_to), quiet.end, { quiet = quiet.copy(end = it.take(5)) }, Modifier.weight(1f), error = if (!hhmm(quiet.end)) "HH:MM" else null)
                        }
                        SwitchLine(stringResource(R.string.nas_silence_all), quiet.silenceAll) { quiet = quiet.copy(silenceAll = it) }
                    }
                }
                SectionCard {
                    SwitchLine(stringResource(R.string.nas_monthly_report), monthly) { monthly = it }
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(enabled = !actions.busy && (!quiet.enabled || (hhmm(quiet.start) && hhmm(quiet.end))), onClick = {
                        actions.run(done = res.getString(R.string.b_saved), after = data.reload) { it.saveNotifySettings(ch, quiet, monthly) }
                    }) { Text(stringResource(R.string.b_save)) }
                    OutlinedButton(enabled = !actions.busy, onClick = {
                        actions.run { val sent = it.testNotification().sent; actions.say(if (sent.isEmpty()) res.getString(R.string.nas_test_none) else res.getString(R.string.nas_test_sent, sent.joinToString(", "))) }
                    }) { Text(stringResource(R.string.nas_test)) }
                }
                Text(stringResource(R.string.nas_channels_web), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

fun hhmm(s: String) = Regex("^([01][0-9]|2[0-3]):[0-5][0-9]$").matches(s)

@Composable
private fun ChannelRow(name: String, configured: Boolean, c: NotifyChannel, set: (NotifyChannel) -> Unit) {
    Column {
        SwitchLine(name, !c.disabled && configured, enabled = configured, sub = if (!configured) stringResource(R.string.nas_ch_not_configured) else null) { set(c.copy(disabled = !it)) }
        if (configured && !c.disabled) Choice(stringResource(R.string.nas_min_level), listOf("" to stringResource(R.string.nas_level_all), "warning" to stringResource(R.string.nas_level_warning), "error" to stringResource(R.string.nas_level_error)),
            c.minLevel.let { if (it == "info") "" else it }, { set(c.copy(minLevel = it)) })
    }
}

// ---- Network ----

@Composable
private fun NetworkScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.network() }
    AreaScreen(stringResource(R.string.nas_network), onBack) { pad ->
        Refreshable(data, pad) { n ->
            ScrollColumn {
                SectionCard {
                    KeyValue(stringResource(R.string.nas_hostname), n.status.hostname)
                    KeyValue(stringResource(R.string.nas_gateway), n.status.gateway.ifEmpty { "—" })
                    KeyValue("DNS", n.status.dns.joinToString(", ").ifEmpty { "—" })
                }
                n.status.interfaces.filter { it.name != "lo" }.forEach { i ->
                    SectionCard(i.name) {
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            StatusBadge(i.state, if (i.state == "up") Tone.Ok else Tone.Neutral)
                            if (i.speed > 0) StatusBadge("${i.speed} Mb/s", Tone.Neutral)
                            if (i.kind.isNotEmpty()) StatusBadge(i.kind, Tone.Neutral)
                        }
                        i.addresses.forEach { Text(it, style = MaterialTheme.typography.bodyMedium) }
                        if (i.mac.isNotEmpty()) KeyValue("MAC", i.mac, mono = true)
                    }
                }
                Text(stringResource(R.string.nas_network_web), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

// ---- Services ----

private class Svc(val list: List<io.github.nathplay15.strataos.net.manage.NasService>, val files: io.github.nathplay15.strataos.net.manage.FileServices)

/** The system services the app turns on and off (the others stay in the web interface). */
val toggledUnits = listOf("smbd.service" to "SMB", "nfs-server.service" to "NFS", "ssh.service" to "SSH")

@Composable
private fun ServicesScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { Svc(it.services(), it.fileServices()) }
    val actions = rememberActions()
    var confirm by remember { mutableStateOf<Pair<String, Boolean>?>(null) }
    AreaScreen(stringResource(R.string.nas_services), onBack) { pad ->
        Refreshable(data, pad) { s ->
            val f = s.files.config
            ScrollColumn {
                SectionCard(stringResource(R.string.nas_file_access)) {
                    toggledUnits.forEach { (unit, label) ->
                        val svc = s.list.firstOrNull { it.unit == unit }
                        if (svc != null) SwitchLine(label, svc.isEnabled || svc.isActive, enabled = !actions.busy, sub = svc.active) { on ->
                            if (on) actions.run(after = data.reload) { it.serviceAction(unit, "enable") } else confirm = unit to false
                        }
                    }
                    SwitchLine("WebDAV", f.webdav, enabled = !actions.busy) { on -> actions.run(after = data.reload) { it.saveFileServices(f.copy(webdav = on)) } }
                    SwitchLine("FTP", f.ftp, enabled = !actions.busy && s.files.ftpInstalled, sub = if (!s.files.ftpInstalled) stringResource(R.string.nas_not_installed) else null) { on ->
                        actions.run(after = data.reload) { it.saveFileServices(f.copy(ftp = on)) }
                    }
                    SwitchLine("SFTP", f.sftp, enabled = !actions.busy && s.files.sftpInstalled) { on -> actions.run(after = data.reload) { it.saveFileServices(f.copy(sftp = on)) } }
                }
                SectionCard(stringResource(R.string.nas_all_services)) {
                    s.list.forEach { svc ->
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(svc.name, style = MaterialTheme.typography.bodyLarge)
                                Text("${svc.active} (${svc.sub}) · ${svc.enabled}", style = MaterialTheme.typography.bodySmall)
                            }
                            if (svc.unit != "strata.service") TextButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.serviceAction(svc.unit, "restart") } }) {
                                Text(stringResource(R.string.nas_restart))
                            }
                        }
                    }
                }
            }
        }
    }
    confirm?.let { (unit, on) ->
        val label = toggledUnits.first { it.first == unit }.second
        if (!on) Confirm(stringResource(R.string.nas_service_off, label), stringResource(if (unit == "ssh.service") R.string.nas_ssh_off_text else R.string.nas_service_off_text, label),
            stringResource(R.string.nas_turn_off), onConfirm = { actions.run(after = data.reload) { it.serviceAction(unit, "disable") } }, onDismiss = { confirm = null })
    }
}

// ---- Security ----

@Composable
private fun SecurityScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.info() }
    AreaScreen(stringResource(R.string.nas_security), onBack) { pad ->
        Refreshable(data, pad) { i ->
            ScrollColumn {
                SectionCard(stringResource(R.string.nas_my_account)) {
                    KeyValue(stringResource(R.string.usr_username), i.user.username)
                    KeyValue(stringResource(R.string.usr_2fa), stringResource(if (i.user.twoFactor) R.string.usr_on else R.string.usr_off))
                    i.device?.let { KeyValue(stringResource(R.string.nas_this_device), it.name) }
                    Text(stringResource(R.string.nas_security_web), style = MaterialTheme.typography.bodySmall)
                }
                Text(stringResource(R.string.nas_security_more), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

// ---- Restore points ----

@Composable
private fun RestoreScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.restorePoints() }
    val actions = rememberActions()
    val res = LocalResources.current
    var rollback by remember { mutableStateOf<String?>(null) }
    var delete by remember { mutableStateOf<String?>(null) }
    AreaScreen(stringResource(R.string.nas_restore), onBack) { pad ->
        Refreshable(data, pad) { r ->
            ScrollColumn {
                Text(stringResource(R.string.nas_restore_help), style = MaterialTheme.typography.bodySmall)
                if (!r.supported) SectionCard { Text(r.reason.ifEmpty { stringResource(R.string.nas_restore_unsupported) }, color = MaterialTheme.colorScheme.error) }
                else {
                    Button(enabled = !actions.busy, onClick = { actions.run(done = res.getString(R.string.nas_point_created), after = data.reload) { it.createRestorePoint() } }) {
                        Text(stringResource(R.string.nas_create_point))
                    }
                    if (r.points.isEmpty()) Text(stringResource(R.string.nas_no_points))
                    r.points.forEach { p ->
                        SectionCard {
                            Text(formatDate(p.created), style = MaterialTheme.typography.bodyLarge)
                            Text(listOf(p.label, p.info).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
                            Row {
                                TextButton(enabled = !p.invalid && !actions.busy, onClick = { rollback = p.name }) { Text(stringResource(R.string.nas_rollback)) }
                                TextButton(enabled = !actions.busy, onClick = { delete = p.name }) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
                            }
                        }
                    }
                }
            }
        }
    }
    val host = Graph.prefs.server?.hostname?.ifEmpty { null } ?: "NAS"
    rollback?.let { p ->
        Confirm(stringResource(R.string.nas_rollback), stringResource(R.string.nas_rollback_text), stringResource(R.string.nas_rollback), typeToConfirm = host,
            onConfirm = { actions.run(done = res.getString(R.string.nas_rebooting)) { it.rollbackRestorePoint(p) } }, onDismiss = { rollback = null })
    }
    delete?.let { p ->
        Confirm(stringResource(R.string.nas_delete_point), stringResource(R.string.nas_delete_point_text), stringResource(R.string.b_delete),
            onConfirm = { actions.run(after = data.reload) { it.deleteRestorePoint(p) } }, onDismiss = { delete = null })
    }
}

// ---- Power ----

@Composable
private fun PowerScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.systemInfo() }
    val actions = rememberActions()
    val res = LocalResources.current
    var confirm by remember { mutableStateOf<String?>(null) }
    AreaScreen(stringResource(R.string.nas_power), onBack) { pad ->
        Refreshable(data, pad) { i ->
            ScrollColumn {
                SectionCard(i.hostname) {
                    Text(stringResource(R.string.nas_power_help), style = MaterialTheme.typography.bodySmall)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(onClick = { confirm = "reboot" }) { Text(stringResource(R.string.nas_reboot)) }
                        Button(onClick = { confirm = "poweroff" }) { Text(stringResource(R.string.nas_poweroff)) }
                    }
                }
            }
        }
    }
    confirm?.let { a ->
        val host = data.value?.hostname?.ifEmpty { null } ?: "NAS"
        Confirm(stringResource(if (a == "reboot") R.string.nas_reboot else R.string.nas_poweroff),
            stringResource(if (a == "reboot") R.string.nas_reboot_text else R.string.nas_poweroff_text, host),
            stringResource(if (a == "reboot") R.string.nas_reboot else R.string.nas_poweroff), typeToConfirm = host,
            onConfirm = { actions.run(done = res.getString(if (a == "reboot") R.string.nas_rebooting else R.string.nas_powering_off)) { it.power(a) } }, onDismiss = { confirm = null })
    }
}

