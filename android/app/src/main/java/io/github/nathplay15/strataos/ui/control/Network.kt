package io.github.nathplay15.strataos.ui.control

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.a
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.d
import io.github.nathplay15.strataos.net.manage.json
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
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import java.time.OffsetDateTime

// Network pages of the web Control Panel natively: WiFi and the NAS
// firewall, applied on trial like the web (the NAS rolls back unless the app
// confirms it can still reach it), and remote access (Tailscale, Cloudflare
// Tunnel, app domains, dynamic DNS).

/**
 * A network change on trial: the countdown until the NAS rolls back, while
 * the app confirms with the token as soon as it reaches the NAS again (409:
 * the change is still settling). Same calls as the web's TrialModal.
 */
@Composable
fun TrialDialog(token: String, deadline: String, confirmPath: String, rollbackPath: String, onClose: () -> Unit) {
    val end = remember(deadline) { runCatching { OffsetDateTime.parse(deadline).toInstant().toEpochMilli() }.getOrDefault(System.currentTimeMillis() + 90_000) }
    var left by remember { mutableIntStateOf(((end - System.currentTimeMillis()) / 1000).toInt()) }
    var state by remember { mutableStateOf("waiting") }
    val actions = rememberActions()
    LaunchedEffect(deadline) { while (left > 0 && state == "waiting") { delay(1000); left = ((end - System.currentTimeMillis()) / 1000).toInt() } }
    LaunchedEffect(token) {
        delay(4000)
        var fails = 0
        while (state != "confirmed" && System.currentTimeMillis() < end) {
            val ok = withContext(Dispatchers.IO) {
                try { api().sendObj("POST", confirmPath, JsonObject(mapOf("token" to kotlinx.serialization.json.JsonPrimitive(token)))); true }
                catch (e: Exception) { if (!(e is ApiException && e.status == 409)) fails++; false }
            }
            if (ok) { state = "confirmed"; break }
            if (fails >= 3) state = "unreachable"
            delay(2500)
        }
    }
    val expired = left <= 0 && state != "confirmed"
    AlertDialog(
        onDismissRequest = {},
        title = { Text(stringResource(R.string.net_trial_title)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                when {
                    state == "confirmed" -> Text(stringResource(R.string.net_trial_kept), color = androidx.compose.ui.graphics.Color(0xFF1E9E5A))
                    expired -> Text(stringResource(R.string.net_trial_expired), color = MaterialTheme.colorScheme.error)
                    else -> {
                        Text(stringResource(R.string.net_trial_wait, left.coerceAtLeast(0)))
                        LinearProgressIndicator(Modifier.fillMaxWidth())
                        if (state == "unreachable") Text(stringResource(R.string.net_trial_unreachable), style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        },
        confirmButton = { if (state == "confirmed" || expired) TextButton(onClick = onClose) { Text(stringResource(R.string.b_ok)) } },
        dismissButton = {
            if (state != "confirmed" && !expired) TextButton(enabled = !actions.busy, onClick = { actions.run(after = onClose) { it.sendObj("POST", rollbackPath) } }) { Text(stringResource(R.string.net_restore_now)) }
        },
    )
}

// ---- WiFi ----

@Composable
fun WifiScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 5000) { it.json("/api/system/network/wifi").o() to it.json("/api/system/network").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    var scan by remember { mutableStateOf<Pair<String, List<JsonObject>>?>(null) }
    var target by remember { mutableStateOf<Triple<String, String, String>?>(null) }
    var psk by remember { mutableStateOf("") }
    var ask by remember { mutableStateOf<Pair<String, JsonObject>?>(null) }
    var trial by remember { mutableStateOf<JsonObject?>(null) }
    AreaScreen(stringResource(R.string.ctl_w_wifi), onBack) { pad ->
        Refreshable(data, pad) { (w, net) ->
            val cfg = net["config"].o()
            fun withWifi(name: String, entry: JsonObject?): JsonObject {
                val list = cfg["wifi"].a().map { it.o() }.filter { it.s("name") != name } + listOfNotNull(entry)
                var ifaces = cfg["interfaces"].a().map { it.o() }
                if (entry != null && ifaces.none { it.s("name") == name }) ifaces = (ifaces + JsonObject(emptyMap()).put("name" to name, "mode" to "dhcp")).sortedBy { it.s("name") }
                return cfg.put("wifi" to JsonArray(list), "interfaces" to JsonArray(ifaces))
            }
            ScrollColumn {
                if (!w.b("wpaInstalled")) SectionCard { Hint(stringResource(R.string.net_wifi_packages)) }
                val ifaces = w.objs("ifaces")
                if (ifaces.isEmpty()) SectionCard { Hint(stringResource(R.string.net_wifi_none)) }
                ifaces.forEach { f ->
                    val st = f["status"].o()
                    val c = f["config"].o()
                    SectionCard {
                        CardHead(f.s("name"), if (st.b("connected")) "${st.s("ssid")} · ${st.l("signalDbm")} dBm" else st.s("state")) {
                            StatusBadge(stringResource(if (st.b("connected")) R.string.net_connected else R.string.net_disconnected), if (st.b("connected")) Tone.Ok else Tone.Neutral)
                        }
                        if (c.b("configured")) KeyValue(stringResource(R.string.net_saved_network), c.s("ssid"))
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            Button(enabled = !actions.busy, onClick = { actions.run { a -> scan = f.s("name") to a.sendObj("POST", "/api/system/network/wifi/scan", JsonObject(emptyMap()).put("iface" to f.s("name"))).o().objs("networks") } }) { Text(stringResource(R.string.net_scan)) }
                            if (c.b("configured")) TextButton(onClick = { ask = r.getString(R.string.net_forget_text, f.s("name")) to withWifi(f.s("name"), null) }) { Text(stringResource(R.string.net_forget), color = MaterialTheme.colorScheme.error) }
                        }
                    }
                }
                scan?.let { (iface, nets) ->
                    SectionCard(stringResource(R.string.net_networks, iface)) {
                        if (nets.isEmpty()) Hint(stringResource(R.string.net_no_networks))
                        nets.forEach { n ->
                            Row(Modifier.fillMaxWidth().clickable { target = Triple(iface, n.s("ssid"), n.s("security")); psk = "" }, verticalAlignment = Alignment.CenterVertically) {
                                Column(Modifier.weight(1f)) {
                                    Text(n.s("ssid").ifEmpty { n.s("bssid") }, style = MaterialTheme.typography.bodyLarge)
                                    Text("${n.s("security")} · ${n.l("signalDbm")} dBm", style = MaterialTheme.typography.bodySmall)
                                }
                            }
                        }
                    }
                }
            }
            target?.let { (iface, ssid, sec) ->
                AlertDialog(onDismissRequest = { target = null }, title = { Text(ssid) }, text = {
                    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (sec != "open") Field(stringResource(R.string.net_wifi_password), psk, { psk = it }, secret = true)
                        Hint(stringResource(R.string.net_trial_text))
                    }
                }, confirmButton = {
                    TextButton(enabled = sec == "open" || psk.length >= 8, onClick = {
                        target = null
                        val entry = JsonObject(emptyMap()).put("name" to iface, "ssid" to ssid, "psk" to (if (sec == "open") "" else psk), "enabled" to true)
                        actions.run { a -> trial = a.sendObj("PUT", "/api/system/network/config", JsonObject(emptyMap()).put("config" to withWifi(iface, entry), "timeout" to 120)).o() }
                    }) { Text(stringResource(R.string.net_apply_trial)) }
                }, dismissButton = { TextButton(onClick = { target = null }) { Text(stringResource(R.string.b_cancel)) } })
            }
        }
    }
    ask?.let { (text, cfg) ->
        Confirm(stringResource(R.string.net_forget), text, stringResource(R.string.net_forget), onDismiss = { ask = null }, onConfirm = {
            actions.run { a -> trial = a.sendObj("PUT", "/api/system/network/config", JsonObject(emptyMap()).put("config" to cfg, "timeout" to 120)).o() }
        })
    }
    trial?.let { t -> TrialDialog(t.s("token"), t.s("deadline"), "/api/system/network/confirm", "/api/system/network/rollback") { trial = null; data.reload() } }
}

// ---- Firewall of the NAS ----

private val ruleActions = listOf("accept", "drop", "reject")
private val protos = listOf("", "tcp", "udp", "tcpudp", "icmp")

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun FirewallScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.json("/api/system/network/firewall").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    var trial by remember { mutableStateOf<JsonObject?>(null) }
    var editing by remember { mutableStateOf<Pair<Int, JsonObject>?>(null) }
    var applyAsk by remember { mutableStateOf(false) }
    var host by remember { mutableStateOf<JsonObject?>(null) }
    AreaScreen(stringResource(R.string.ctl_w_firewall), onBack) { pad ->
        Refreshable(data, pad) { fw ->
            val cfg = fw["config"].o()
            val h = host ?: cfg["host"].o()
            val rules = h["rules"].a().map { it.o() }
            fun setHost(x: JsonObject) { host = x }
            ScrollColumn {
                if (!fw.b("nft")) SectionCard { Hint(stringResource(R.string.net_fw_no_nft)) }
                fw["trial"]?.let { it as? JsonObject }?.let { t ->
                    SectionCard {
                        Text(stringResource(R.string.net_fw_on_trial, whenText(t.s("deadline"))), style = MaterialTheme.typography.bodyMedium)
                        OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.sendObj("POST", "/api/system/network/firewall/rollback") } }) { Text(stringResource(R.string.net_restore_now)) }
                    }
                }
                SectionCard(stringResource(R.string.net_fw_nas)) {
                    SwitchLine(stringResource(R.string.net_fw_filter), h.b("enabled"), sub = stringResource(R.string.net_fw_filter_help)) { on ->
                        setHost(if (on && rules.isEmpty()) fw["defaultHost"].o().put("enabled" to true) else h.put("enabled" to on))
                    }
                    if (h.b("enabled")) {
                        Hint(stringResource(R.string.net_fw_always, fw["webPorts"].a().joinToString(", ") { it.toString() }))
                        Choice(stringResource(R.string.net_fw_policy), listOf("drop" to r.getString(R.string.net_fw_drop), "reject" to r.getString(R.string.net_fw_reject), "accept" to r.getString(R.string.net_fw_accept)),
                            h.s("policyIn").ifEmpty { "drop" }, { setHost(h.put("policyIn" to it)) })
                        SwitchLine(stringResource(R.string.net_fw_log), h.b("logDrops")) { setHost(h.put("logDrops" to it)) }
                        rules.forEachIndexed { i, ru ->
                            Row(Modifier.fillMaxWidth().clickable { editing = i to ru }, verticalAlignment = Alignment.CenterVertically) {
                                StatusBadge(ru.s("action"), when (ru.s("action")) { "accept" -> Tone.Ok; else -> Tone.Bad })
                                Column(Modifier.weight(1f).padding(start = 8.dp)) {
                                    Text(listOf(ru.s("dir"), ru.s("proto").ifEmpty { "any" }, ru.s("ports")).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodyMedium)
                                    Text(listOf(ru.s("source"), ru.s("comment")).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
                                }
                                if (ru.b("disabled")) StatusBadge(stringResource(R.string.usr_off), Tone.Neutral)
                            }
                        }
                        TextButton(onClick = { editing = -1 to JsonObject(emptyMap()).put("dir" to "in", "action" to "accept", "proto" to "tcp") }) { Text(stringResource(R.string.net_fw_add_rule)) }
                    }
                }
                val guests = fw.objs("guests")
                if (guests.isNotEmpty()) SectionCard(stringResource(R.string.net_fw_guests)) {
                    guests.forEach { g -> KeyValue(g.s("name"), (if (g.b("firewall")) r.getString(R.string.usr_on) else r.getString(R.string.usr_off)) + " · " + g.s("state")) }
                    Hint(stringResource(R.string.net_fw_guests_help))
                }
                Button(enabled = host != null && !actions.busy && fw["trial"] !is JsonObject, onClick = { applyAsk = true }) { Text(stringResource(R.string.net_apply_trial)) }
            }
            editing?.let { (i, ru) ->
                RuleDialog(ru, onDismiss = { editing = null }, onDelete = if (i >= 0) ({ setHost(h.put("rules" to JsonArray(rules.filterIndexed { k, _ -> k != i }))); editing = null }) else null) { nr ->
                    setHost(h.put("rules" to JsonArray(if (i >= 0) rules.mapIndexed { k, x -> if (k == i) nr else x } else rules + nr)))
                    editing = null
                }
            }
            if (applyAsk) Confirm(stringResource(R.string.net_fw_apply_title), stringResource(R.string.net_fw_apply_text), stringResource(R.string.net_apply_trial), destructive = false,
                onDismiss = { applyAsk = false }, onConfirm = {
                    val body = JsonObject(emptyMap()).put("config" to cfg.put("host" to h), "timeout" to 90)
                    actions.run(after = { host = null; data.reload() }) { a ->
                        val res = a.sendObj("PUT", "/api/system/network/firewall", body).o()
                        if (res.s("token").isNotEmpty()) trial = res else actions.say(r.getString(R.string.net_fw_saved))
                    }
                })
        }
    }
    trial?.let { t -> TrialDialog(t.s("token"), t.s("deadline"), "/api/system/network/firewall/confirm", "/api/system/network/firewall/rollback") { trial = null; data.reload() } }
}


@Composable
private fun RuleDialog(rule: JsonObject, onDismiss: () -> Unit, onDelete: (() -> Unit)?, onSave: (JsonObject) -> Unit) {
    val r = LocalResources.current
    var action by remember { mutableStateOf(rule.s("action").ifEmpty { "accept" }) }
    var proto by remember { mutableStateOf(rule.s("proto")) }
    var ports by remember { mutableStateOf(rule.s("ports")) }
    var source by remember { mutableStateOf(rule.s("source")) }
    var comment by remember { mutableStateOf(rule.s("comment")) }
    var on by remember { mutableStateOf(!rule.b("disabled")) }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(stringResource(R.string.net_fw_rule)) }, text = {
        Column(Modifier.verticalScrollable(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Choice(stringResource(R.string.ctl_actions), ruleActions.map { it to it }, action, { action = it })
            Choice(stringResource(R.string.net_fw_proto), protos.map { it to it.ifEmpty { r.getString(R.string.net_fw_any) } }, proto, { proto = it })
            if (proto in setOf("tcp", "udp", "tcpudp")) Field(stringResource(R.string.ctl_port), ports, { ports = it.trim() }, help = "22, 80, 8000-8100")
            Field(stringResource(R.string.net_fw_source), source, { source = it.trim() }, help = stringResource(R.string.net_fw_source_help))
            Field(stringResource(R.string.net_fw_comment), comment, { comment = it })
            SwitchLine(stringResource(R.string.usr_enabled), on) { on = it }
        }
    }, confirmButton = {
        TextButton(onClick = { onSave(rule.put("dir" to rule.s("dir").ifEmpty { "in" }, "action" to action, "proto" to proto, "ports" to ports, "source" to source, "comment" to comment, "disabled" to !on)) }) { Text(stringResource(R.string.b_ok)) }
    }, dismissButton = {
        Row {
            if (onDelete != null) TextButton(onClick = onDelete) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
            TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) }
        }
    })
}

@Composable
private fun Modifier.verticalScrollable() = this.verticalScroll(androidx.compose.foundation.rememberScrollState())

// ---- Remote access: Tailscale, Cloudflare Tunnel, app domains, dynamic DNS ----

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun RemoteScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 8000) { a ->
        listOf(runCatching { a.json("/api/remote/tailscale").o() }.getOrNull(), runCatching { a.json("/api/remote/tunnel").o() }.getOrNull(),
            runCatching { a.json("/api/remote/domains").o() }.getOrNull(), runCatching { a.json("/api/vpn").o() }.getOrNull())
    }
    val actions = rememberActions()
    val r = LocalResources.current
    val ctx = LocalContext.current
    var job by remember { mutableStateOf<NasJob?>(null) }
    var authKey by remember { mutableStateOf("") }
    var domain by remember { mutableStateOf<JsonObject?>(null) }
    var removing by remember { mutableStateOf<JsonObject?>(null) }
    fun install(c: String) = actions.run { a -> job = a.startJob("POST", "/api/system/components/$c/install") }
    AreaScreen(stringResource(R.string.ctl_w_remote), onBack) { pad ->
        Refreshable(data, pad) { (ts, tun, dom, vpn) ->
            ScrollColumn {
                // Tailscale
                if (ts != null) SectionCard("Tailscale") {
                    val st = ts["status"].o()
                    if (!ts.b("installed")) Button(enabled = !actions.busy, onClick = { install("tailscale") }) { Text(stringResource(R.string.nas_install)) }
                    else {
                        CardHead(st.s("name").ifEmpty { st.s("state") }, st.s("tailnet").ifEmpty { null }) {
                            StatusBadge(stringResource(if (st.b("loggedIn")) R.string.net_connected else R.string.net_disconnected), if (st.b("loggedIn")) Tone.Ok else Tone.Neutral)
                        }
                        if (st.b("loggedIn")) {
                            KeyValue("IP", st["ips"].a().joinToString(", ") { it.toString().trim('"') })
                            KeyValue(stringResource(R.string.net_devices_online), "${st.l("online")} / ${st.objs("peers").size}")
                            TextButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.sendObj("POST", "/api/remote/tailscale/logout") } }) { Text(stringResource(R.string.net_sign_out), color = MaterialTheme.colorScheme.error) }
                        } else {
                            Field(stringResource(R.string.net_ts_authkey), authKey, { authKey = it.trim() }, secret = true, help = stringResource(R.string.net_ts_authkey_help))
                            Button(enabled = !actions.busy, onClick = {
                                actions.run(after = data.reload) { a ->
                                    val url = a.sendObj("POST", "/api/remote/tailscale/login", JsonObject(emptyMap()).put("authKey" to authKey)).o().s("authUrl")
                                    authKey = ""
                                    if (url.isNotEmpty()) withContext(Dispatchers.Main) { runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) } }
                                }
                            }) { Text(stringResource(R.string.net_sign_in)) }
                        }
                    }
                }
                // Cloudflare Tunnel
                if (tun != null) SectionCard("Cloudflare Tunnel") {
                    val st = tun["status"].o()
                    if (!st.b("installed")) Button(enabled = !actions.busy, onClick = { install("tunnel") }) { Text(stringResource(R.string.nas_install)) }
                    else {
                        CardHead(st.s("service").ifEmpty { "cloudflared" }, st.s("tunnelId").ifEmpty { null }) {
                            StatusBadge(stringResource(if (st.b("connected")) R.string.net_connected else R.string.net_disconnected), if (st.b("connected")) Tone.Ok else Tone.Neutral)
                        }
                        var token by remember(tun) { mutableStateOf(tun["config"].o().s("token")) }
                        var on by remember(tun) { mutableStateOf(tun["config"].o().b("enabled")) }
                        SwitchLine(stringResource(R.string.usr_enabled), on) { on = it }
                        Field(stringResource(R.string.net_tunnel_token), token, { token = it.trim() }, secret = true)
                        TextButton(enabled = !actions.busy, onClick = { actions.run(done = r.getString(R.string.b_saved), after = data.reload) { it.sendObj("PUT", "/api/remote/tunnel", tun["config"].o().put("enabled" to on, "token" to token)) } }) { Text(stringResource(R.string.b_save)) }
                    }
                }
                // App domains
                if (dom != null) SectionCard(stringResource(R.string.net_domains)) {
                    SwitchLine(stringResource(R.string.net_domains_on), dom.b("enabled"), sub = stringResource(R.string.net_public_ip, dom.s("publicIp"))) { on ->
                        actions.run(after = data.reload) { it.sendObj("PUT", "/api/remote/domains/settings", JsonObject(emptyMap()).put("enabled" to on, "email" to dom.s("email"))) }
                    }
                    dom.objs("domains").forEach { d ->
                        val cert = d["cert"].o()
                        Row(Modifier.fillMaxWidth().clickable { domain = d }, verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(d.s("host"), style = MaterialTheme.typography.bodyLarge)
                                Text(listOf(d.s("app").ifEmpty { d.s("target").ifEmpty { d.s("kind") } }, cert.s("source"), d.s("problem").ifEmpty { d.s("certError") }).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
                            }
                            StatusBadge(cert.s("source").ifEmpty { "none" }, if (cert.s("source") == "letsencrypt") Tone.Ok else Tone.Warn)
                        }
                    }
                    TextButton(onClick = { domain = JsonObject(emptyMap()).put("kind" to "app") }) { Text(stringResource(R.string.net_publish_app)) }
                    domain?.let { d -> DomainDialog(d, dom.objs("apps"), onDismiss = { domain = null }, onRemove = { removing = d; domain = null },
                        onCert = { actions.run(after = data.reload) { a -> a.sendObj("POST", "/api/remote/domains/${d.s("id")}/certificate") }; domain = null }) { body ->
                        domain = null
                        actions.run(done = r.getString(R.string.b_saved), after = data.reload) { a -> if (d.s("id").isEmpty()) a.sendObj("POST", "/api/remote/domains", body) else a.sendObj("PUT", "/api/remote/domains/${d.s("id")}", body) }
                    } }
                }
                // Dynamic DNS (part of the VPN settings in the web)
                if (vpn != null) DdnsCard(vpn["ddns"].o(), actions.busy) { body -> actions.run(done = r.getString(R.string.b_saved), after = data.reload) { it.sendObj("PUT", "/api/ddns", body) } }
            }
        }
    }
    removing?.let { d -> Confirm(r.getString(R.string.ctl_delete_title, d.s("host")), stringResource(R.string.net_domain_remove_text), stringResource(R.string.b_delete), onDismiss = { removing = null },
        onConfirm = { actions.run(after = data.reload) { it.sendObj("DELETE", "/api/remote/domains/${d.s("id")}") } }) }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

@Composable
private fun DomainDialog(d: JsonObject, apps: List<JsonObject>, onDismiss: () -> Unit, onRemove: () -> Unit, onCert: () -> Unit, onSave: (JsonObject) -> Unit) {
    val r = LocalResources.current
    var host by remember { mutableStateOf(d.s("host")) }
    var kind by remember { mutableStateOf(d.s("kind").ifEmpty { "app" }) }
    var app by remember { mutableStateOf(d.s("app").ifEmpty { apps.firstOrNull()?.s("stack").orEmpty() }) }
    var target by remember { mutableStateOf(d.s("target")) }
    var lan by remember { mutableStateOf(d.b("lanOnly")) }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(if (d.s("id").isEmpty()) stringResource(R.string.net_publish_app) else d.s("host")) }, text = {
        Column(Modifier.verticalScrollable(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Field(stringResource(R.string.net_host_name), host, { host = it.trim() }, help = stringResource(R.string.net_host_name_help))
            Choice(stringResource(R.string.net_domain_kind), listOf("app" to r.getString(R.string.a_applications), "url" to r.getString(R.string.net_domain_url), "ui" to r.getString(R.string.area_web)), kind, { kind = it })
            if (kind == "app") Choice(stringResource(R.string.a_applications), apps.map { it.s("stack") to it.s("name").ifEmpty { it.s("stack") } }, app, { app = it })
            if (kind == "url") Field("URL", target, { target = it.trim() }, help = "http://192.168.1.20:8080")
            SwitchLine(stringResource(R.string.net_lan_only), lan) { lan = it }
            if (d.s("id").isNotEmpty()) Row {
                TextButton(onClick = onCert) { Text(stringResource(R.string.net_get_cert)) }
                TextButton(onClick = onRemove) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
            }
        }
    }, confirmButton = {
        TextButton(enabled = host.isNotEmpty(), onClick = {
            onSave(JsonObject(emptyMap()).put("id" to d.s("id").ifEmpty { null }, "host" to host, "kind" to kind, "app" to app, "target" to target, "lanOnly" to lan,
                "selfSigned" to d.b("selfSigned"), "allowedIPs" to (d["allowedIPs"] ?: JsonArray(emptyList()))))
        }) { Text(stringResource(R.string.b_save)) }
    }, dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
}

@Composable
private fun DdnsCard(dd: JsonObject, busy: Boolean, save: (JsonObject) -> Unit) {
    val r = LocalResources.current
    var provider by remember(dd) { mutableStateOf(dd.s("provider")) }
    var hostname by remember(dd) { mutableStateOf(dd.s("hostname")) }
    var token by remember(dd) { mutableStateOf(dd.s("token")) }
    var zone by remember(dd) { mutableStateOf(dd.s("zoneId")) }
    var url by remember(dd) { mutableStateOf(dd.s("url")) }
    SectionCard(stringResource(R.string.net_ddns)) {
        Choice(stringResource(R.string.net_ddns_provider), listOf("" to r.getString(R.string.net_none), "duckdns" to "DuckDNS", "dynv6" to "dynv6", "cloudflare" to "Cloudflare", "custom" to r.getString(R.string.net_custom_url)), provider, { provider = it })
        if (provider.isNotEmpty() && provider != "custom") {
            Field(stringResource(R.string.net_host_name), hostname, { hostname = it.trim() })
            Field(stringResource(R.string.net_token), token, { token = it }, secret = true)
        }
        if (provider == "cloudflare") Field("Zone ID", zone, { zone = it.trim() })
        if (provider == "custom") Field(stringResource(R.string.net_custom_url), url, { url = it.trim() }, help = "https://…?ip={ip}")
        if (dd.s("lastUpdate").isNotEmpty()) Hint(listOf(dd.s("lastIp"), whenText(dd.s("lastUpdate")), dd.s("lastResult")).filter { it.isNotEmpty() }.joinToString(" · "))
        TextButton(enabled = !busy, onClick = { save(dd.put("provider" to provider, "hostname" to hostname, "token" to token, "zoneId" to zone, "url" to url)) }) { Text(stringResource(R.string.b_save)) }
    }
}
