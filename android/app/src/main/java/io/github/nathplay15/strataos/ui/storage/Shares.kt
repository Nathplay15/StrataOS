package io.github.nathplay15.strataos.ui.storage

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.FolderShared
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.NasUser
import io.github.nathplay15.strataos.net.QuotaEntry
import io.github.nathplay15.strataos.net.StorageLocation
import io.github.nathplay15.strataos.net.bool
import io.github.nathplay15.strataos.net.createShare
import io.github.nathplay15.strataos.net.deleteShare
import io.github.nathplay15.strataos.net.groups
import io.github.nathplay15.strataos.net.locations
import io.github.nathplay15.strataos.net.newShare
import io.github.nathplay15.strataos.net.permissions
import io.github.nathplay15.strataos.net.saveShare
import io.github.nathplay15.strataos.net.setUserQuota
import io.github.nathplay15.strataos.net.shareObjects
import io.github.nathplay15.strataos.net.str
import io.github.nathplay15.strataos.net.userQuotas
import io.github.nathplay15.strataos.net.users
import io.github.nathplay15.strataos.net.with
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.TextDialog
import io.github.nathplay15.strataos.ui.UsageBar
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

private class ShareData(val shares: List<JsonObject>, val users: List<NasUser>, val groups: List<String>, val locations: List<StorageLocation>)

private val shareNameRe = Regex("^[A-Za-z0-9][A-Za-z0-9 _\\-.]{0,62}$")

/** Permission of everyone named in [perms] cycled none → ro → rw (the values the NAS takes). */
fun nextPermission(p: String?): String = when (p) { "ro" -> "rw"; "rw" -> "none"; else -> "ro" }

@Composable
fun SharesTab() {
    val act = rememberActions()
    val load = rememberLoad(Unit) {
        val a = api()
        ShareData(a.shareObjects(), runCatching { a.users() }.getOrDefault(emptyList()), runCatching { a.groups().filter { !it.system }.map { it.name } }.getOrDefault(emptyList()),
            runCatching { a.locations() }.getOrDefault(emptyList()))
    }
    var editing by remember { mutableStateOf<JsonObject?>(null) }
    var creating by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf<String?>(null) }
    Refreshable(load) { d ->
        CardList {
            item { Button(onClick = { creating = true }, enabled = d.locations.isNotEmpty()) { Text(stringResource(R.string.a_new_share)) } }
            if (d.shares.isEmpty()) item { Text(stringResource(R.string.a_no_shares), color = MaterialTheme.colorScheme.onSurfaceVariant) }
            items(d.shares, key = { it.str("name") }) { sh ->
                val name = sh.str("name")
                val protocols = listOfNotNull(if (sh.bool("smb")) "SMB" else null, if (sh.bool("nfs")) "NFS" else null, if (sh.bool("timeMachine")) "Time Machine" else null)
                ItemCard(name, if (sh.bool("locked")) "locked" else if (sh.bool("frozen")) "frozen" else "", listOf(sh.str("path"), protocols.joinToString("/")).filter { it.isNotEmpty() }.joinToString(" · "),
                    onClick = { editing = sh },
                    menu = { close ->
                        DropdownMenuItem({ Text(stringResource(R.string.a_edit)) }, { close(); editing = sh })
                        DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, { close(); deleting = name })
                    }) {
                    val perms = sh.permissions().filterValues { it != "none" }
                    if (perms.isNotEmpty()) Text(perms.entries.joinToString(", ") { "${it.key} (${it.value})" }, style = MaterialTheme.typography.bodySmall)
                    val quota = sh.str("quotaGb").toDoubleOrNull() ?: 0.0
                    val used = sh.str("used").toLongOrNull() ?: 0
                    if (quota > 0) UsageBar(used, (quota * 1e9).toLong())
                    if (sh.bool("recycleBin")) Text(stringResource(R.string.a_with_recycle_bin), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
        editing?.let { sh -> ShareEditor(sh, d, onDismiss = { editing = null }) { updated -> act.run(load.reload) { api().saveShare(sh.str("name"), updated) } } }
        if (creating) NewShareDialog(d.locations, onDismiss = { creating = false }) { created ->
            creating = false
            act.run(load.reload) { api().createShare(created) }
        }
    }
    deleting?.let { n ->
        ConfirmDialog(stringResource(R.string.a_delete_share, n), stringResource(R.string.a_delete_share_text), stringResource(R.string.a_delete), danger = true,
            onDismiss = { deleting = null }, onConfirm = { act.run(load.reload) { api().deleteShare(n) } })
    }
}

@Composable
private fun NewShareDialog(locations: List<StorageLocation>, onDismiss: () -> Unit, onCreate: (JsonObject) -> Unit) {
    var name by remember { mutableStateOf("") }
    var loc by remember { mutableStateOf(locations.first()) }
    var smb by remember { mutableStateOf(true) }
    var nfs by remember { mutableStateOf(false) }
    var bin by remember { mutableStateOf(true) }
    FullDialog(stringResource(R.string.a_new_share), onDismiss) {
        OutlinedTextField(name, { name = it }, label = { Text(stringResource(R.string.a_share_name)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            isError = name.isNotEmpty() && !shareNameRe.matches(name.trim()))
        Text(stringResource(R.string.a_share_location), style = MaterialTheme.typography.titleSmall)
        locations.forEach { l ->
            Row(Modifier.fillMaxWidth().selectable(loc == l) { loc = l }, verticalAlignment = Alignment.CenterVertically) {
                RadioButton(loc == l, null)
                Text("${l.label} — " + stringResource(R.string.a_free_space, formatBytes(l.free)), Modifier.padding(start = 8.dp))
            }
        }
        Text(stringResource(R.string.a_share_folder, loc.path + "/" + name.trim()), style = MaterialTheme.typography.bodySmall)
        SwitchRow("SMB (Windows, macOS)", smb) { smb = it }
        SwitchRow("NFS (Linux)", nfs) { nfs = it }
        SwitchRow(stringResource(R.string.a_recycle_bin), bin) { bin = it }
        Text(stringResource(R.string.a_share_perm_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Button(onClick = { onCreate(newShare(name.trim(), loc.path + "/" + name.trim(), "", smb, nfs, bin, emptyMap())) },
            enabled = shareNameRe.matches(name.trim()), modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.create)) }
    }
}

/** Edits a share: protocols, recycle bin, quota and who may read or write. */
@Composable
private fun ShareEditor(sh: JsonObject, d: ShareData, onDismiss: () -> Unit, onSave: (JsonObject) -> Unit) {
    var description by remember { mutableStateOf(sh.str("description")) }
    var smb by remember { mutableStateOf(sh.bool("smb")) }
    var nfs by remember { mutableStateOf(sh.bool("nfs")) }
    var bin by remember { mutableStateOf(sh.bool("recycleBin")) }
    var guest by remember { mutableStateOf(sh.bool("guest")) }
    var quota by remember { mutableStateOf(sh.str("quotaGb").let { if (it == "0" || it.isEmpty()) "" else it }) }
    var perms by remember { mutableStateOf(sh.permissions()) }
    FullDialog(sh.str("name"), onDismiss) {
        Text(sh.str("path"), style = MaterialTheme.typography.bodySmall)
        OutlinedTextField(description, { description = it }, label = { Text(stringResource(R.string.a_description)) }, modifier = Modifier.fillMaxWidth())
        SwitchRow("SMB", smb) { smb = it }
        SwitchRow("NFS", nfs) { nfs = it }
        SwitchRow(stringResource(R.string.a_recycle_bin), bin) { bin = it }
        SwitchRow(stringResource(R.string.a_guest_access), guest) { guest = it }
        OutlinedTextField(quota, { quota = it.filter { c -> c.isDigit() || c == '.' } }, label = { Text(stringResource(R.string.a_share_quota)) }, singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal), modifier = Modifier.fillMaxWidth())
        HorizontalDivider()
        Text(stringResource(R.string.a_permissions), style = MaterialTheme.typography.titleSmall)
        Text(stringResource(R.string.a_permissions_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        val who = d.users.filter { !it.disabled }.map { it.username to (it.fullName.ifEmpty { it.username }) } + d.groups.map { "@$it" to stringResource(R.string.a_group, it) }
        who.forEach { (key, label) ->
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Text(label, Modifier.weight(1f))
                listOf("none" to R.string.a_perm_none, "ro" to R.string.a_perm_ro, "rw" to R.string.a_perm_rw).forEach { (v, l) ->
                    FilterChip(selected = (perms[key] ?: "none") == v, onClick = { perms = perms + (key to v) }, label = { Text(stringResource(l)) }, modifier = Modifier.padding(start = 4.dp))
                }
            }
        }
        Button(onClick = {
            onDismiss()
            onSave(sh.with(
                "description" to JsonPrimitive(description), "smb" to JsonPrimitive(smb), "nfs" to JsonPrimitive(nfs), "recycleBin" to JsonPrimitive(bin),
                "guest" to JsonPrimitive(guest), "quotaGb" to JsonPrimitive(quota.toDoubleOrNull() ?: 0.0),
                "permissions" to JsonObject(perms.filterValues { it != "none" }.mapValues { JsonPrimitive(it.value) }),
            ))
        }, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.a_save)) }
    }
}

@Composable
private fun SwitchRow(label: String, on: Boolean, set: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Text(label, Modifier.weight(1f))
        Switch(on, set)
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun FullDialog(title: String, onDismiss: () -> Unit, content: @Composable () -> Unit) {
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), topBar = {
            TopAppBar(title = { Text(title) }, navigationIcon = { IconButton(onClick = onDismiss) { Icon(Icons.Default.Close, stringResource(R.string.cancel)) } })
        }) { pad ->
            Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) { content() }
        }
    }
}

/** Per-user quotas: pick a user, then a limit per volume or dataset. */
@Composable
fun QuotasTab() {
    val act = rememberActions()
    val users = rememberLoad(Unit) { api().users() }
    var user by remember { mutableStateOf<String?>(null) }
    var editing by remember { mutableStateOf<QuotaEntry?>(null) }
    val quotas = rememberLoad(user) { user?.let { api().userQuotas(it) } ?: emptyList() }
    val u = user
    if (u == null) {
        Refreshable(users) { list ->
            if (list.isEmpty()) EmptyState(Icons.Default.Person, stringResource(R.string.a_no_users))
            else androidx.compose.foundation.lazy.LazyColumn {
                item { Text(stringResource(R.string.a_quotas_intro), Modifier.padding(16.dp), style = MaterialTheme.typography.bodySmall) }
                items(list, key = { it.username }) { x ->
                    ListItem(headlineContent = { Text(x.fullName.ifEmpty { x.username }) }, supportingContent = { Text(x.username) },
                        leadingContent = { Icon(Icons.Default.Person, null) }, modifier = Modifier.clickable { user = x.username })
                    HorizontalDivider()
                }
            }
        }
        return
    }
    io.github.nathplay15.strataos.ui.BackTo { user = null }
    Column(Modifier.fillMaxSize()) {
        Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            OutlinedButton(onClick = { user = null }) { Text(stringResource(R.string.a_all_users)) }
            Text(u, Modifier.padding(start = 12.dp), style = MaterialTheme.typography.titleMedium)
        }
        Refreshable(quotas) { list ->
            if (list.isEmpty()) EmptyState(Icons.Default.FolderShared, stringResource(R.string.a_no_quota_targets))
            else CardList {
                items(list, key = { it.kind + it.target }) { q ->
                    ItemCard(q.name, if (!q.supported) "" else if (q.enabled) "" else "off", "${q.kind} · ${q.mountPoint}", onClick = if (q.supported && q.enabled) ({ editing = q }) else null) {
                        when {
                            !q.supported -> Text(stringResource(R.string.a_quota_unsupported), style = MaterialTheme.typography.bodySmall)
                            !q.enabled -> Text(stringResource(R.string.a_quota_disabled), style = MaterialTheme.typography.bodySmall)
                            q.limit > 0 -> UsageBar(q.used, q.limit)
                            else -> Text(stringResource(R.string.a_quota_none, formatBytes(q.used)), style = MaterialTheme.typography.bodySmall)
                        }
                    }
                }
            }
        }
    }
    editing?.let { q ->
        TextDialog(stringResource(R.string.a_quota_for, u, q.name), stringResource(R.string.a_quota_gb), initial = if (q.limit > 0) "%.1f".format(java.util.Locale.ROOT, q.limit / 1073741824.0) else "",
            confirm = stringResource(R.string.a_save), valid = { it.isEmpty() || it.toDoubleOrNull() != null }, number = true, onDismiss = { editing = null }) { v ->
            act.run(quotas.reload) { api().setUserQuota(u, q.kind, q.target, ((v.toDoubleOrNull() ?: 0.0) * 1073741824.0).toLong()) }
        }
    }
}
