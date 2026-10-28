package io.github.nathplay15.strataos.ui.vpn

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.QrCode
import androidx.compose.material.icons.filled.VpnKey
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.MyVpn
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.PeerReq
import io.github.nathplay15.strataos.net.manage.PeerStatus
import io.github.nathplay15.strataos.net.manage.VpnClient
import io.github.nathplay15.strataos.net.manage.VpnConfigFile
import io.github.nathplay15.strataos.net.manage.VpnPeer
import io.github.nathplay15.strataos.net.manage.VpnServer
import io.github.nathplay15.strataos.net.manage.VpnServerReq
import io.github.nathplay15.strataos.net.manage.addMyVpnDevice
import io.github.nathplay15.strataos.net.manage.addPeer
import io.github.nathplay15.strataos.net.manage.deleteMyVpnDevice
import io.github.nathplay15.strataos.net.manage.deletePeer
import io.github.nathplay15.strataos.net.manage.installComponent
import io.github.nathplay15.strataos.net.manage.myVpn
import io.github.nathplay15.strataos.net.manage.myVpnConfig
import io.github.nathplay15.strataos.net.manage.peerConfig
import io.github.nathplay15.strataos.net.manage.savePeer
import io.github.nathplay15.strataos.net.manage.saveVpn
import io.github.nathplay15.strataos.net.manage.setVpnClient
import io.github.nathplay15.strataos.net.manage.toReq
import io.github.nathplay15.strataos.net.manage.vpn
import io.github.nathplay15.strataos.net.manage.vpnClient
import io.github.nathplay15.strataos.net.manage.vpnClientCheck
import io.github.nathplay15.strataos.net.manage.vpnClientReconnect
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.QrCode
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.adminkit.shareTextFile
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate

// VPN: the WireGuard server of the NAS (its devices with their QR codes,
// expiry, access) and the outgoing tunnel. A user who is not an administrator
// sees their own devices, when the administrator allows them.

/** A valid expiry typed by the user: "" (never) or YYYY-MM-DD. */
fun validExpiry(s: String): Boolean = s.isEmpty() || runCatching { java.time.LocalDate.parse(s) }.isSuccess

/** "Handshake 3 min ago" style age, from a Unix time (0 = never). */
fun ageMinutes(unix: Long, nowMs: Long = System.currentTimeMillis()): Long? = if (unix <= 0) null else ((nowMs / 1000 - unix) / 60).coerceAtLeast(0)

@Composable
fun VpnArea(back: () -> Unit) {
    if (Graph.prefs.server?.isAdmin == true) AdminVpn(back) else MyVpnScreen(back)
}

@Composable
private fun handshakeText(st: PeerStatus?): String {
    val m = st?.let { ageMinutes(it.handshake) } ?: return stringResource(R.string.vpn_never_connected)
    return when {
        m < 3 -> stringResource(R.string.vpn_connected_now)
        m < 120 -> stringResource(R.string.vpn_seen_min, m.toInt())
        else -> stringResource(R.string.vpn_seen_hours, (m / 60).toInt())
    }
}

@Composable
private fun AdminVpn(back: () -> Unit) {
    val data = rememberData(Unit) { it.vpn() }
    val client = rememberData(Unit) { it.vpnClient() }
    var adding by remember { mutableStateOf(false) }
    var settings by remember { mutableStateOf(false) }
    var showing by remember { mutableStateOf<VpnConfigFile?>(null) }
    var job by remember { mutableStateOf<NasJob?>(null) }
    val actions = rememberActions()
    val reload = { data.reload(); client.reload() }
    AreaScreen(stringResource(R.string.area_vpn), back, fab = {
        if (data.value?.installed == true) ExtendedFloatingActionButton(onClick = { adding = true }, icon = { Icon(Icons.Default.Add, null) }, text = { Text(stringResource(R.string.vpn_add_device)) })
    }) { pad ->
        Refreshable(data, pad) { v ->
            if (!v.installed) Empty(Icons.Default.VpnKey, stringResource(R.string.vpn_not_installed)) {
                Button(onClick = { actions.run { job = it.installComponent("vpn") } }, enabled = !actions.busy) { Text(stringResource(R.string.vpn_install)) }
            } else ScrollColumn {
                ServerCard(v, onEdit = { settings = true }, reload = reload)
                SectionCard(stringResource(R.string.vpn_devices)) {
                    if (v.config.peers.isEmpty()) Text(stringResource(R.string.vpn_no_devices), style = MaterialTheme.typography.bodySmall)
                    v.config.peers.forEach { p ->
                        PeerRow(p, v.status[p.publicKey], v.inactive[p.id], showConfig = { actions.run { showing = it.peerConfig(p.id) } },
                            toggle = { on -> actions.run(after = reload) { it.savePeer(p.id, p.toReq(disabled = !on)) } },
                            delete = { actions.run(after = reload) { it.deletePeer(p.id) } })
                        HorizontalDivider()
                    }
                }
                client.value?.let { OutgoingCard(it, client.reload) }
                Text(stringResource(R.string.vpn_no_openvpn), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
    data.value?.let { v ->
        if (adding) AddPeerDialog(v, onDismiss = { adding = false }) { r -> adding = false; actions.run(after = reload) { val n = it.addPeer(r); showing = VpnConfigFile(n.config, n.peer.name) } }
        if (settings) ServerSettingsDialog(v, onDismiss = { settings = false }) { r -> settings = false; actions.run(after = reload) { it.saveVpn(r) } }
    }
    showing?.let { ConfigDialog(it) { showing = null } }
    job?.let { j -> JobDialog(j) { job = null; reload() } }
}

@Composable
private fun ServerCard(v: VpnServer, onEdit: () -> Unit, reload: () -> Unit) {
    val actions = rememberActions()
    val c = v.config
    val req = VpnServerReq(c.enabled, c.port, c.subnet, c.endpoint, c.lan, c.defaultMode, c.userDevices, c.expiryWarnDays)
    SectionCard(stringResource(R.string.vpn_server)) {
        SwitchLine(stringResource(R.string.vpn_server_on), c.enabled, enabled = !actions.busy) { on -> actions.run(after = reload) { it.saveVpn(req.copy(enabled = on)) } }
        KeyValue(stringResource(R.string.vpn_endpoint), (c.endpoint.ifEmpty { v.ddnsHost.ifEmpty { "—" } }) + ":" + c.port)
        KeyValue(stringResource(R.string.vpn_network), c.subnet + if (v.serverIp.isNotEmpty()) " (${v.serverIp})" else "")
        if (c.lan.isNotEmpty()) KeyValue(stringResource(R.string.vpn_lan), c.lan)
        KeyValue(stringResource(R.string.vpn_default_access), modeName(c.defaultMode))
        KeyValue(stringResource(R.string.vpn_user_devices), stringResource(if (c.userDevices) R.string.vpn_allowed else R.string.vpn_not_allowed))
        if (c.endpoint.isEmpty()) Text(stringResource(R.string.vpn_endpoint_missing), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
        OutlinedButton(onClick = onEdit) { Text(stringResource(R.string.vpn_edit_settings)) }
    }
}

@Composable
fun modeName(m: String): String = when (m) {
    "nas" -> stringResource(R.string.vpn_mode_nas)
    "all" -> stringResource(R.string.vpn_mode_all)
    else -> stringResource(R.string.vpn_mode_lan)
}

@Composable
private fun PeerRow(p: VpnPeer, st: PeerStatus?, inactive: String?, showConfig: () -> Unit, toggle: ((Boolean) -> Unit)?, delete: () -> Unit) {
    var confirm by remember { mutableStateOf(false) }
    Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(p.name, style = MaterialTheme.typography.bodyLarge)
                if (p.site) StatusBadge(stringResource(R.string.vpn_site), Tone.Accent)
                when {
                    inactive == "expired" || p.expired -> StatusBadge(stringResource(R.string.vpn_expired), Tone.Bad)
                    inactive != null -> StatusBadge(stringResource(R.string.vpn_inactive), Tone.Warn)
                    p.disabled -> StatusBadge(stringResource(R.string.vpn_disabled), Tone.Neutral)
                }
            }
            Text(listOf(p.ip, if (p.site) p.subnets.joinToString(", ") else modeName(p.mode), p.owner.ifEmpty { null }?.let { stringResource(R.string.vpn_owner, it) } ?: "")
                .filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
            Text(handshakeText(st) + (st?.takeIf { it.rx + it.tx > 0 }?.let { " · ↓ ${formatBytes(it.rx)} ↑ ${formatBytes(it.tx)}" } ?: ""),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (p.expires.isNotEmpty()) Text(stringResource(R.string.vpn_expires, formatDate(p.expires)), style = MaterialTheme.typography.bodySmall)
        }
        IconButton(onClick = showConfig) { Icon(Icons.Default.QrCode, stringResource(R.string.vpn_show_config)) }
        if (toggle != null) Switch(!p.disabled, toggle, enabled = !p.expired)
        IconButton(onClick = { confirm = true }) { Icon(Icons.Default.Delete, stringResource(R.string.vpn_revoke)) }
    }
    if (confirm) Confirm(stringResource(R.string.vpn_revoke), stringResource(R.string.vpn_revoke_text, p.name), stringResource(R.string.vpn_revoke),
        onConfirm = delete, onDismiss = { confirm = false }, typeToConfirm = p.name)
}

@Composable
private fun AddPeerDialog(v: VpnServer, onDismiss: () -> Unit, add: (PeerReq) -> Unit) {
    var name by remember { mutableStateOf("") }
    var owner by remember { mutableStateOf("") }
    var mode by remember { mutableStateOf(v.config.defaultMode.ifEmpty { "lan" }) }
    var expires by remember { mutableStateOf("") }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(stringResource(R.string.vpn_add_device)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Field(stringResource(R.string.vpn_device_name), name, { name = it })
                Choice(stringResource(R.string.vpn_owner_label), listOf("" to stringResource(R.string.vpn_owner_admins)) + v.users.filter { !it.disabled }.map { it.username to it.username }, owner, { owner = it })
                Choice(stringResource(R.string.vpn_access), listOf("nas" to stringResource(R.string.vpn_mode_nas), "lan" to stringResource(R.string.vpn_mode_lan), "all" to stringResource(R.string.vpn_mode_all)), mode, { mode = it })
                Field(stringResource(R.string.vpn_expiry), expires, { expires = it.trim() }, help = stringResource(R.string.vpn_expiry_help), error = if (!validExpiry(expires)) stringResource(R.string.vpn_expiry_help) else null)
            }
        },
        confirmButton = { TextButton(enabled = name.isNotBlank() && validExpiry(expires), onClick = { add(PeerReq(name.trim(), owner = owner, mode = mode, expires = expires)) }) { Text(stringResource(R.string.vm_create_btn)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
}

@Composable
private fun ServerSettingsDialog(v: VpnServer, onDismiss: () -> Unit, save: (VpnServerReq) -> Unit) {
    val c = v.config
    var port by remember { mutableStateOf(c.port.toString()) }
    var endpoint by remember { mutableStateOf(c.endpoint) }
    var subnet by remember { mutableStateOf(c.subnet.ifEmpty { "10.8.0.0/24" }) }
    var lan by remember { mutableStateOf(c.lan.ifEmpty { v.lanGuess }) }
    var mode by remember { mutableStateOf(c.defaultMode.ifEmpty { "lan" }) }
    var users by remember { mutableStateOf(c.userDevices) }
    var warn by remember { mutableStateOf(c.expiryWarnDays.toString()) }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(stringResource(R.string.vpn_edit_settings)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Field(stringResource(R.string.vpn_endpoint), endpoint, { endpoint = it.trim() }, help = stringResource(R.string.vpn_endpoint_help))
                Field(stringResource(R.string.vpn_port), port, { port = it.filter(Char::isDigit) }, number = true)
                Field(stringResource(R.string.vpn_network), subnet, { subnet = it.trim() }, help = if (c.peers.isNotEmpty()) stringResource(R.string.vpn_network_locked) else null, enabled = c.peers.isEmpty())
                Field(stringResource(R.string.vpn_lan), lan, { lan = it.trim() })
                Choice(stringResource(R.string.vpn_default_access), listOf("nas" to stringResource(R.string.vpn_mode_nas), "lan" to stringResource(R.string.vpn_mode_lan), "all" to stringResource(R.string.vpn_mode_all)), mode, { mode = it })
                SwitchLine(stringResource(R.string.vpn_user_devices), users) { users = it }
                Field(stringResource(R.string.vpn_warn_days), warn, { warn = it.filter { ch -> ch.isDigit() || ch == '-' } }, number = true, help = stringResource(R.string.vpn_warn_help))
            }
        },
        confirmButton = {
            TextButton(onClick = { save(VpnServerReq(c.enabled, port.toIntOrNull() ?: 51820, subnet, endpoint, lan, mode, users, warn.toIntOrNull() ?: 0)) }) { Text(stringResource(R.string.b_save)) }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
}

/** A device's WireGuard configuration: QR code to scan with the WireGuard app, or the file to share. */
@Composable
fun ConfigDialog(c: VpnConfigFile, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    AlertDialog(onDismissRequest = onDismiss, title = { Text(c.name) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Text(stringResource(R.string.vpn_scan_help), style = MaterialTheme.typography.bodySmall)
                Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) { QrCode(c.config, stringResource(R.string.vpn_qr_desc, c.name)) }
                Text(stringResource(R.string.vpn_secret_warning), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_ok)) } },
        dismissButton = {
            TextButton(onClick = { shareTextFile(ctx, confName(c.name), c.config) }) { Text(stringResource(R.string.vpn_share_file)) }
        })
}

/** WireGuard wants short interface names: the file is named after the device. */
fun confName(device: String): String = device.replace(Regex("[^A-Za-z0-9_=+.-]"), "_").take(15).ifEmpty { "strataos" } + ".conf"

@Composable
private fun OutgoingCard(c: VpnClient, reload: () -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    SectionCard(stringResource(R.string.vpn_outgoing)) {
        if (c.config.endpoint.isEmpty()) {
            Text(stringResource(R.string.vpn_outgoing_none), style = MaterialTheme.typography.bodySmall)
            return@SectionCard
        }
        SwitchLine(c.config.name.ifEmpty { c.config.endpoint }, c.config.enabled, enabled = !actions.busy,
            sub = stringResource(if (c.config.scope == "all") R.string.vpn_scope_all else R.string.vpn_scope_apps)) { on ->
            actions.run(after = reload) { it.setVpnClient(on) }
        }
        if (c.config.enabled) {
            KeyValue(stringResource(R.string.vpn_tunnel), stringResource(if (c.routing) R.string.vpn_tunnel_ok else R.string.vpn_tunnel_down))
            c.status?.let { st -> KeyValue(stringResource(R.string.vpn_last_handshake), handshakeText(st)); KeyValue("↓ / ↑", "${formatBytes(st.rx)} / ${formatBytes(st.tx)}") }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedButton(enabled = !actions.busy, onClick = { actions.run { val ip = it.vpnClientCheck().publicIp; actions.say(res.getString(R.string.vpn_public_ip, ip)) } }) {
                    Text(stringResource(R.string.vpn_check_ip))
                }
                OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = reload) { it.vpnClientReconnect() } }) { Text(stringResource(R.string.vm_reconnect)) }
            }
        }
    }
}

/** A user's own devices, when the administrator lets users add them. */
@Composable
private fun MyVpnScreen(back: () -> Unit) {
    val data = rememberData(Unit) { it.myVpn() }
    var adding by remember { mutableStateOf(false) }
    var showing by remember { mutableStateOf<VpnConfigFile?>(null) }
    val actions = rememberActions()
    AreaScreen(stringResource(R.string.area_vpn), back, fab = {
        val m: MyVpn? = data.value
        if (m != null && m.allowed && m.devices.size < m.max) ExtendedFloatingActionButton(onClick = { adding = true }, icon = { Icon(Icons.Default.Add, null) }, text = { Text(stringResource(R.string.vpn_add_device)) })
    }) { pad ->
        Refreshable(data, pad) { m ->
            if (!m.allowed && m.devices.isEmpty()) Empty(Icons.Default.VpnKey, stringResource(R.string.vpn_user_not_allowed))
            else if (m.devices.isEmpty()) Empty(Icons.Default.VpnKey, stringResource(R.string.vpn_no_devices))
            else ScrollColumn {
                SectionCard(stringResource(R.string.vpn_my_devices)) {
                    m.devices.forEach { p ->
                        PeerRow(p, m.status[p.publicKey], m.inactive[p.id], showConfig = { actions.run { showing = it.myVpnConfig(p.id) } }, toggle = null,
                            delete = { actions.run(after = data.reload) { it.deleteMyVpnDevice(p.id) } })
                        HorizontalDivider()
                    }
                }
            }
        }
    }
    if (adding) {
        var name by remember { mutableStateOf("") }
        AlertDialog(onDismissRequest = { adding = false }, title = { Text(stringResource(R.string.vpn_add_device)) },
            text = { Field(stringResource(R.string.vpn_device_name), name, { name = it }) },
            confirmButton = {
                TextButton(enabled = name.isNotBlank(), onClick = {
                    adding = false
                    actions.run(after = data.reload) { val n = it.addMyVpnDevice(name.trim()); showing = VpnConfigFile(n.config, n.peer.name) }
                }) { Text(stringResource(R.string.vm_create_btn)) }
            },
            dismissButton = { TextButton(onClick = { adding = false }) { Text(stringResource(R.string.b_cancel)) } })
    }
    showing?.let { ConfigDialog(it) { showing = null } }
}
