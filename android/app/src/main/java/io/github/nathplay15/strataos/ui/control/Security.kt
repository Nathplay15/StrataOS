package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Devices
import androidx.compose.material.icons.filled.GppGood
import androidx.compose.material.icons.filled.History
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
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
import io.github.nathplay15.strataos.net.manage.antivirus
import io.github.nathplay15.strataos.net.manage.a
import io.github.nathplay15.strataos.net.manage.json
import io.github.nathplay15.strataos.net.manage.sendObj
import io.github.nathplay15.strataos.net.manage.advisor
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.block
import io.github.nathplay15.strataos.net.manage.unblock
import io.github.nathplay15.strataos.net.manage.deleteQuarantined
import io.github.nathplay15.strataos.net.manage.endOtherSessions
import io.github.nathplay15.strataos.net.manage.endSession
import io.github.nathplay15.strataos.net.manage.fixAdvice
import io.github.nathplay15.strataos.net.manage.guard
import io.github.nathplay15.strataos.net.manage.ignoreAdvice
import io.github.nathplay15.strataos.net.manage.l
import io.github.nathplay15.strataos.net.manage.logins
import io.github.nathplay15.strataos.net.manage.o
import io.github.nathplay15.strataos.net.manage.objs
import io.github.nathplay15.strataos.net.manage.restoreQuarantined
import io.github.nathplay15.strataos.net.manage.runAdvisor
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.net.manage.sessions
import io.github.nathplay15.strataos.ui.LocalOpenWeb
import io.github.nathplay15.strataos.ui.ThinBar
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes

// Security pages of the web Control Panel: the advisor, address blocking,
// sessions, the sign-in journal and the antivirus quarantine.

private class Ask(val title: String, val text: String, val confirm: String, val destructive: Boolean, val typed: String? = null, val run: () -> Unit)

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun AdvisorScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.advisor() }
    val actions = rememberActions()
    val r = LocalResources.current
    val web = LocalOpenWeb.current
    var ask by remember { mutableStateOf<Ask?>(null) }
    AreaScreen(stringResource(R.string.ctl_advisor), onBack) { pad ->
        Refreshable(data, pad) { run ->
            ScrollColumn {
                val score = run.l("score").toInt()
                SectionCard {
                    CardHead(stringResource(R.string.ctl_score, score), whenText(run.s("time")).takeIf { it.isNotEmpty() }?.let { stringResource(R.string.ctl_checked_at, it) }) {
                        Button(enabled = !actions.busy, onClick = { actions.run(done = r.getString(R.string.ctl_checks_done), after = data.reload) { it.runAdvisor() } }) { Text(stringResource(R.string.ctl_check_now)) }
                    }
                    ThinBar(score / 100f, color = when { score >= 80 -> androidx.compose.ui.graphics.Color(0xFF1E9E5A); score >= 50 -> androidx.compose.ui.graphics.Color(0xFFE0A100); else -> MaterialTheme.colorScheme.error })
                }
                val items = run.objs("items")
                if (items.none { !it.b("ignored") }) Text(stringResource(R.string.ctl_nothing_to_fix), style = MaterialTheme.typography.bodyMedium)
                items.sortedBy { it.b("ignored") }.forEach { it0 ->
                    SectionCard {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            StatusBadge(severityText(r, it0.s("severity")), if (it0.b("ignored")) Tone.Neutral else toneOf(it0.s("severity")))
                            Text(it0.s("title"), Modifier.weight(1f), style = MaterialTheme.typography.titleSmall)
                        }
                        if (it0.s("detail").isNotEmpty()) Text(it0.s("detail"), style = MaterialTheme.typography.bodySmall)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            if (it0.s("fix").isNotEmpty() && !it0.b("ignored")) Button(enabled = !actions.busy, onClick = {
                                ask = Ask(it0.s("title"), r.getString(R.string.ctl_fix_text), r.getString(R.string.ctl_fix), false) {
                                    actions.run(done = r.getString(R.string.ctl_fixed), after = data.reload) { a -> a.fixAdvice(it0.s("fix")) }
                                }
                            }) { Text(stringResource(R.string.ctl_fix)) }
                            if (it0.s("link").isNotEmpty()) TextButton(onClick = { web(it0.s("link"), it0.s("title")) }) { Text(stringResource(R.string.ctl_open_setting)) }
                            TextButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { a -> a.ignoreAdvice(it0.s("id"), !it0.b("ignored")) } }) {
                                Text(stringResource(if (it0.b("ignored")) R.string.ctl_watch_again else R.string.ctl_ignore))
                            }
                        }
                    }
                }
            }
        }
    }
    ask?.let { a -> Confirm(a.title, a.text, a.confirm, onConfirm = a.run, onDismiss = { ask = null }, destructive = a.destructive) }
}

private fun severityText(r: android.content.res.Resources, s: String): String = r.getString(when (s) {
    "high" -> R.string.ctl_sev_high
    "medium" -> R.string.ctl_sev_medium
    "low" -> R.string.ctl_sev_low
    else -> R.string.ctl_sev_info
})

@Composable
fun GuardScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 10000) { it.guard() }
    val actions = rememberActions()
    val r = LocalResources.current
    val web = LocalOpenWeb.current
    var ip by remember { mutableStateOf("") }
    AreaScreen(stringResource(R.string.ctl_guard), onBack, actions = {
        TextButton(onClick = { web("/system/guard", null) }) { Text(stringResource(R.string.ctl_settings_web)) }
    }) { pad ->
        Refreshable(data, pad) { g ->
            val cfg = g["config"].o()
            ScrollColumn {
                SectionCard {
                    CardHead(stringResource(R.string.ctl_guard_state), stringResource(R.string.ctl_your_ip, g.s("myIp"))) {
                        StatusBadge(stringResource(if (cfg.b("enabled")) R.string.usr_on else R.string.usr_off), if (cfg.b("enabled")) Tone.Ok else Tone.Warn)
                    }
                    KeyValue(stringResource(R.string.ctl_guard_rule), stringResource(R.string.ctl_guard_rule_v, cfg.l("maxFailures").toInt(), cfg.l("windowMin").toInt(), cfg.l("blockMin").toInt()))
                }
                SectionCard(stringResource(R.string.ctl_block_ip)) {
                    Field(stringResource(R.string.ctl_ip_address), ip, { ip = it.trim() })
                    OutlinedButton(enabled = ip.isNotEmpty() && !actions.busy, onClick = { actions.run(done = r.getString(R.string.ctl_blocked_done), after = { ip = ""; data.reload() }) { it.block(ip) } }) { Text(stringResource(R.string.ctl_block)) }
                }
                val blocks = g.objs("blocks")
                SectionCard(stringResource(R.string.ctl_blocked_n, blocks.size)) {
                    if (blocks.isEmpty()) Text(stringResource(R.string.ctl_none_blocked), style = MaterialTheme.typography.bodyMedium)
                    blocks.forEach { b ->
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            androidx.compose.foundation.layout.Column(Modifier.weight(1f)) {
                                Text(b.s("ip"), style = MaterialTheme.typography.bodyLarge)
                                Text(listOf(b.s("service"), b.s("user"), stringResource(R.string.ctl_until, whenText(b.s("until")))).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
                            }
                            TextButton(enabled = !actions.busy, onClick = { actions.run(done = r.getString(R.string.ctl_unblocked_done), after = data.reload) { it.unblock(b.s("ip")) } }) { Text(stringResource(R.string.ctl_unblock)) }
                        }
                    }
                }
            }
        }
    }
}

@Composable
fun SessionsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 10000) { it.sessions() }
    val actions = rememberActions()
    val r = LocalResources.current
    var ask by remember { mutableStateOf<Ask?>(null) }
    AreaScreen(stringResource(R.string.ctl_sessions), onBack) { pad ->
        Refreshable(data, pad) { list ->
            if (list.isEmpty()) Empty(Icons.Default.Devices, stringResource(R.string.ctl_no_sessions))
            else ScrollColumn {
                Hint(stringResource(R.string.ctl_sessions_hint))
                list.forEach { ss ->
                    SectionCard {
                        CardHead(ss.s("device").ifEmpty { ss.s("ip") }, "${ss.s("user")} · ${ss.s("ip")}") {
                            if (ss.b("current")) StatusBadge(stringResource(R.string.ctl_this_session), Tone.Accent)
                        }
                        KeyValue(stringResource(R.string.ctl_signed_in), whenText(ss.s("created")))
                        KeyValue(stringResource(R.string.ctl_expires), whenText(ss.s("expires")))
                        if (!ss.b("current")) TextButton(enabled = !actions.busy, onClick = {
                            ask = Ask(r.getString(R.string.ctl_end_session), r.getString(R.string.ctl_end_session_text, ss.s("user"), ss.s("ip")), r.getString(R.string.ctl_end), true) {
                                actions.run(after = data.reload) { it.endSession(ss.s("id")) }
                            }
                        }) { Text(stringResource(R.string.ctl_end_session), color = MaterialTheme.colorScheme.error) }
                    }
                }
                OutlinedButton(enabled = !actions.busy, onClick = {
                    ask = Ask(r.getString(R.string.ctl_end_others), r.getString(R.string.ctl_end_others_text), r.getString(R.string.ctl_end), true) {
                        actions.run(after = data.reload) { a -> val n = a.endOtherSessions().l("ended"); actions.say(r.getString(R.string.ctl_ended_n, n.toInt())) }
                    }
                }) { Text(stringResource(R.string.ctl_end_others)) }
            }
        }
    }
    ask?.let { a -> Confirm(a.title, a.text, a.confirm, onConfirm = a.run, onDismiss = { ask = null }, destructive = a.destructive) }
}

@Composable
fun LoginsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.logins() }
    val r = LocalResources.current
    AreaScreen(stringResource(R.string.ctl_logins), onBack) { pad ->
        Refreshable(data, pad) { list ->
            if (list.isEmpty()) Empty(Icons.Default.History, stringResource(R.string.ctl_no_logins))
            else ScrollColumn {
                list.forEach { e ->
                    SectionCard {
                        CardHead(e.s("user"), whenText(e.s("time")) + " · " + e.s("ip")) {
                            StatusBadge(stringResource(if (e.b("ok")) R.string.ctl_login_ok else R.string.ctl_login_failed), if (e.b("ok")) Tone.Ok else Tone.Bad)
                        }
                        Text(listOf(howText(r, e.s("how")), if (e.b("newDevice")) r.getString(R.string.ctl_new_device) else "", e.s("ua")).filter { it.isNotEmpty() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        }
    }
}

private fun howText(r: android.content.res.Resources, how: String): String = when (how) {
    "password" -> r.getString(R.string.usr_password)
    "twofactor" -> r.getString(R.string.ctl_how_2fa)
    "key" -> r.getString(R.string.ctl_how_key)
    "passkey" -> r.getString(R.string.usr_passkeys)
    "oidc" -> r.getString(R.string.ctl_how_oidc)
    else -> how
}

@Composable
fun AntivirusScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 15000) { it.antivirus() to runCatching { it.json("/api/ransomware").o() }.getOrNull() }
    val actions = rememberActions()
    val r = LocalResources.current
    val web = LocalOpenWeb.current
    var ask by remember { mutableStateOf<Ask?>(null) }
    AreaScreen(stringResource(R.string.ctl_antivirus), onBack, actions = {
        TextButton(onClick = { web("/system/antivirus", null) }) { Text(stringResource(R.string.ctl_settings_web)) }
    }) { pad ->
        Refreshable(data, pad) { (av, rw) ->
            val st = av["status"].o()
            ScrollColumn {
                if (rw != null) SectionCard {
                    CardHead(stringResource(R.string.rw_title)) {
                        StatusBadge(stringResource(if (rw.b("enabled")) R.string.usr_on else R.string.usr_off), if (rw.b("enabled")) Tone.Ok else Tone.Warn)
                    }
                    val frozen = rw["frozen"].a().map { (it as kotlinx.serialization.json.JsonPrimitive).content }
                    val blocked = rw["blocked"].a().map { (it as kotlinx.serialization.json.JsonPrimitive).content }
                    if (frozen.isEmpty() && blocked.isEmpty() && !rw.b("nfsPaused")) Hint(stringResource(R.string.rw_none))
                    if (frozen.isNotEmpty()) Text(stringResource(R.string.rw_frozen), style = MaterialTheme.typography.bodyMedium)
                    frozen.forEach { sh -> OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { a -> a.sendObj("POST", "/api/ransomware/unfreeze", kotlinx.serialization.json.buildJsonObject { put("share", kotlinx.serialization.json.JsonPrimitive(sh)) }) } }) { Text(stringResource(R.string.rw_restore_share, sh)) } }
                    if (rw.b("nfsPaused")) {
                        Text(stringResource(R.string.rw_nfs), style = MaterialTheme.typography.bodyMedium)
                        OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { a -> a.sendObj("POST", "/api/ransomware/nfs-resume") } }) { Text(stringResource(R.string.rw_restore_nfs)) }
                    }
                    if (blocked.isNotEmpty()) Text(stringResource(R.string.rw_blocked), style = MaterialTheme.typography.bodyMedium)
                    blocked.forEach { u -> OutlinedButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { a -> a.sendObj("POST", "/api/ransomware/unblock", kotlinx.serialization.json.buildJsonObject { put("user", kotlinx.serialization.json.JsonPrimitive(u)) }) } }) { Text(stringResource(R.string.rw_unblock, u)) } }
                }
                SectionCard {
                    CardHead(stringResource(R.string.ctl_antivirus), st.s("version").ifEmpty { null }) {
                        StatusBadge(stringResource(if (st.b("installed")) R.string.ctl_installed else R.string.nas_not_installed), if (st.b("installed")) Tone.Ok else Tone.Neutral)
                    }
                    Hint(stringResource(R.string.ctl_antivirus_hint))
                }
                val q = av.objs("quarantine")
                if (q.isEmpty()) Empty(Icons.Default.GppGood, stringResource(R.string.ctl_quarantine_empty))
                q.forEach { f ->
                    SectionCard {
                        CardHead(f.s("path").substringAfterLast('/'), f.s("virus")) { StatusBadge(formatBytes(f.l("size")), Tone.Neutral) }
                        Text(f.s("path") + " · " + whenText(f.s("date")), style = MaterialTheme.typography.bodySmall)
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(enabled = !actions.busy, onClick = {
                                ask = Ask(r.getString(R.string.ctl_restore_file), r.getString(R.string.ctl_restore_file_text, f.s("path")), r.getString(R.string.ctl_restore), true) {
                                    actions.run(after = data.reload) { a -> val p = a.restoreQuarantined(f.s("id")).s("path"); actions.say(r.getString(R.string.ctl_restored_to, p)) }
                                }
                            }) { Text(stringResource(R.string.ctl_restore)) }
                            TextButton(enabled = !actions.busy, onClick = {
                                ask = Ask(r.getString(R.string.ctl_delete_title, f.s("path").substringAfterLast('/')), r.getString(R.string.ctl_delete_forever_text), r.getString(R.string.b_delete), true) {
                                    actions.run(done = r.getString(R.string.ctl_deleted_forever), after = data.reload) { a -> a.deleteQuarantined(f.s("id")) }
                                }
                            }) { Text(stringResource(R.string.b_delete), color = MaterialTheme.colorScheme.error) }
                        }
                    }
                }
            }
        }
    }
    ask?.let { a -> Confirm(a.title, a.text, a.confirm, onConfirm = a.run, onDismiss = { ask = null }, destructive = a.destructive, typeToConfirm = a.typed) }
}
