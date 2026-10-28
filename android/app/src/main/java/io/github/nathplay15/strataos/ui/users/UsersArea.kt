package io.github.nathplay15.strataos.ui.users

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Group
import androidx.compose.material.icons.filled.Person
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.NasGroup
import io.github.nathplay15.strataos.net.manage.NasUser
import io.github.nathplay15.strataos.net.manage.PasswordProblem
import io.github.nathplay15.strataos.net.manage.UserCreate
import io.github.nathplay15.strataos.net.manage.UserUpdate
import io.github.nathplay15.strataos.net.manage.createGroup
import io.github.nathplay15.strataos.net.manage.createUser
import io.github.nathplay15.strataos.net.manage.deleteGroup
import io.github.nathplay15.strataos.net.manage.deleteUser
import io.github.nathplay15.strataos.net.manage.groups
import io.github.nathplay15.strataos.net.manage.passwordProblem
import io.github.nathplay15.strataos.net.manage.reset2fa
import io.github.nathplay15.strataos.net.manage.setGroupMembers
import io.github.nathplay15.strataos.net.manage.setSharePermission
import io.github.nathplay15.strataos.net.manage.setUserQuota
import io.github.nathplay15.strataos.net.manage.shares
import io.github.nathplay15.strataos.net.manage.updateUser
import io.github.nathplay15.strataos.net.manage.userNameRe
import io.github.nathplay15.strataos.net.manage.userQuotas
import io.github.nathplay15.strataos.net.manage.users
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Data
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Field
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
import io.github.nathplay15.strataos.ui.formatBytes

// Users and groups: accounts (profile, role, groups, password, quotas, share
// permissions, deletion) and the groups with their members.
// Routes: "users", "groups", "user:<name>", "new".

private class Accounts(val users: List<NasUser>, val groups: List<NasGroup>)

@Composable
fun UsersArea(back: () -> Unit) {
    var route by rememberSaveable { mutableStateOf("users") }
    val data = rememberData(Unit) { Accounts(it.users(), it.groups()) }
    val (kind, name) = route.split(":", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
    val home = { route = "users"; data.reload() }
    val u = data.value?.users?.firstOrNull { it.username == name }
    when {
        kind == "new" -> UserForm(null, data.value?.groups.orEmpty(), onBack = home)
        kind == "user" && u != null -> UserForm(u, data.value?.groups.orEmpty(), onBack = home)
        else -> Lists(data, kind == "groups", back, { route = it }) { route = if (it) "groups" else "users" }
    }
}

@Composable
private fun Lists(data: Data<Accounts>, groupsTab: Boolean, back: () -> Unit, go: (String) -> Unit, tab: (Boolean) -> Unit) {
    var newGroup by remember { mutableStateOf(false) }
    var editGroup by remember { mutableStateOf<NasGroup?>(null) }
    val actions = rememberActions()
    AreaScreen(stringResource(R.string.area_users), back, fab = {
        ExtendedFloatingActionButton(onClick = { if (groupsTab) newGroup = true else go("new") }, icon = { Icon(Icons.Default.Add, null) },
            text = { Text(stringResource(if (groupsTab) R.string.usr_new_group else R.string.usr_new_user)) })
    }) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            PrimaryTabRow(selectedTabIndex = if (groupsTab) 1 else 0) {
                Tab(!groupsTab, { tab(false) }, text = { Text(stringResource(R.string.usr_users)) })
                Tab(groupsTab, { tab(true) }, text = { Text(stringResource(R.string.usr_groups)) })
            }
            Refreshable(data, androidx.compose.foundation.layout.PaddingValues()) { a ->
                if (!groupsTab) LazyColumn(Modifier.fillMaxSize()) {
                    items(a.users, key = { it.username }) { u ->
                        ListItem(modifier = Modifier.readable().clickable { go("user:${u.username}") },
                            leadingContent = { Icon(Icons.Default.Person, null) },
                            headlineContent = { Text(if (u.fullName.isNotEmpty()) "${u.fullName} (${u.username})" else u.username) },
                            supportingContent = { if (u.groups.isNotEmpty()) Text(u.groups.joinToString(", ")) },
                            trailingContent = {
                                Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                                    if (u.admin) StatusBadge(stringResource(R.string.usr_admin), Tone.Accent)
                                    if (u.disabled) StatusBadge(stringResource(R.string.usr_disabled), Tone.Bad)
                                    if (u.directory) StatusBadge(stringResource(R.string.usr_directory), Tone.Neutral)
                                }
                            })
                        HorizontalDivider(Modifier.readable())
                    }
                } else if (a.groups.isEmpty()) Empty(Icons.Default.Group, stringResource(R.string.usr_no_groups))
                else LazyColumn(Modifier.fillMaxSize()) {
                    items(a.groups, key = { it.name }) { g ->
                        ListItem(modifier = Modifier.readable().clickable { editGroup = g }, leadingContent = { Icon(Icons.Default.Group, null) },
                            headlineContent = { Text(g.name) },
                            supportingContent = { Text(if (g.members.isEmpty()) stringResource(R.string.usr_no_members) else g.members.joinToString(", ")) })
                        HorizontalDivider(Modifier.readable())
                    }
                }
            }
        }
    }
    val users = data.value?.users.orEmpty().filter { !it.directory }.map { it.username }
    if (newGroup) GroupDialog(null, users, onDismiss = { newGroup = false }) { n, m -> newGroup = false; actions.run(after = data.reload) { it.createGroup(n, m) } }
    editGroup?.let { g ->
        GroupDialog(g, users, onDismiss = { editGroup = null }, delete = { editGroup = null; actions.run(after = data.reload) { it.deleteGroup(g.name) } }) { _, m ->
            editGroup = null
            actions.run(after = data.reload) { it.setGroupMembers(g.name, m) }
        }
    }
}

@Composable
private fun GroupDialog(g: NasGroup?, users: List<String>, onDismiss: () -> Unit, delete: (() -> Unit)? = null, save: (String, List<String>) -> Unit) {
    var name by remember { mutableStateOf(g?.name ?: "") }
    var members by remember { mutableStateOf(g?.members?.toSet() ?: emptySet()) }
    var confirmDelete by remember { mutableStateOf(false) }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(g?.name ?: stringResource(R.string.usr_new_group)) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                if (g == null) Field(stringResource(R.string.cam_name), name, { name = it.lowercase() }, error = if (name.isNotEmpty() && !userNameRe.matches(name)) stringResource(R.string.usr_name_rule) else null)
                Text(stringResource(R.string.usr_members), style = MaterialTheme.typography.labelLarge)
                users.forEach { u ->
                    Row(Modifier.fillMaxWidth().clickable { members = if (u in members) members - u else members + u }, verticalAlignment = Alignment.CenterVertically) {
                        Checkbox(u in members, { members = if (it) members + u else members - u })
                        Text(u)
                    }
                }
                if (delete != null) TextButton(onClick = { confirmDelete = true }) { Text(stringResource(R.string.usr_delete_group), color = MaterialTheme.colorScheme.error) }
            }
        },
        confirmButton = { TextButton(enabled = userNameRe.matches(name), onClick = { save(name, members.sorted()) }) { Text(stringResource(R.string.b_save)) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } })
    if (confirmDelete && g != null && delete != null) Confirm(stringResource(R.string.usr_delete_group), stringResource(R.string.usr_delete_group_text, g.name), stringResource(R.string.b_delete),
        onConfirm = delete, onDismiss = { confirmDelete = false }, typeToConfirm = g.name)
}

@Composable
fun passwordError(p: String): String? = when (passwordProblem(p)) {
    PasswordProblem.TooShort -> stringResource(R.string.usr_pass_short)
    PasswordProblem.Forbidden -> stringResource(R.string.usr_pass_chars)
    null -> null
}

/** Creates a user ([u] null) or edits one. */
@OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
@Composable
private fun UserForm(u: NasUser?, allGroups: List<NasGroup>, onBack: () -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    val me = Graph.prefs.server?.username
    var username by rememberSaveable { mutableStateOf(u?.username ?: "") }
    var fullName by rememberSaveable { mutableStateOf(u?.fullName ?: "") }
    var email by rememberSaveable { mutableStateOf(u?.email ?: "") }
    var role by rememberSaveable { mutableStateOf(u?.role ?: "user") }
    var enabled by rememberSaveable { mutableStateOf(!(u?.disabled ?: false)) }
    var groups by remember { mutableStateOf(u?.groups?.toSet() ?: setOf("users")) }
    var ssh by rememberSaveable { mutableStateOf(u?.ssh ?: false) }
    var docker by rememberSaveable { mutableStateOf(u?.docker ?: false) }
    var pass by remember { mutableStateOf("") }
    var pass2 by remember { mutableStateOf("") }
    var deleting by remember { mutableStateOf(false) }
    var removeHome by remember { mutableStateOf(false) }
    var reset2fa by remember { mutableStateOf(false) }
    val self = u != null && u.username == me
    val update = { p: String -> UserUpdate(fullName, email, role, !enabled, groups.sorted(), ssh, docker, p) }
    val passOk = passwordError(pass) == null && pass == pass2

    AreaScreen(u?.username ?: stringResource(R.string.usr_new_user), onBack, actions = {
        if (u != null && !self) IconButton(onClick = { deleting = true }) { Icon(Icons.Default.Delete, stringResource(R.string.usr_delete)) }
    }) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                SectionCard(stringResource(R.string.usr_profile)) {
                    if (u == null) Field(stringResource(R.string.usr_username), username, { username = it.lowercase().trim() },
                        error = if (username.isNotEmpty() && !userNameRe.matches(username)) stringResource(R.string.usr_name_rule) else null)
                    Field(stringResource(R.string.usr_full_name), fullName, { fullName = it })
                    Field(stringResource(R.string.usr_email), email, { email = it.trim() })
                    Choice(stringResource(R.string.usr_role), listOf("user" to stringResource(R.string.usr_role_user), "admin" to stringResource(R.string.usr_admin)), role, { role = it }, enabled = !self)
                    if (u != null) SwitchLine(stringResource(R.string.usr_enabled), enabled, enabled = !self) { enabled = it }
                    if (u?.directory != true) {
                        Text(stringResource(R.string.usr_groups), style = MaterialTheme.typography.labelLarge)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            allGroups.filter { !it.system || it.name == "users" }.forEach { g ->
                                FilterChip(selected = g.name in groups, onClick = { groups = if (g.name in groups) groups - g.name else groups + g.name }, label = { Text(g.name) })
                            }
                        }
                        SwitchLine(stringResource(R.string.usr_ssh), ssh, sub = stringResource(R.string.usr_ssh_help)) { ssh = it }
                    }
                    SwitchLine(stringResource(R.string.usr_docker), docker, sub = stringResource(R.string.usr_docker_help)) { docker = it }
                    if (u != null) Button(enabled = !actions.busy, onClick = { actions.run(done = res.getString(R.string.b_saved)) { it.updateUser(u.username, update("")) } }) {
                        Text(stringResource(R.string.b_save))
                    }
                }
                if (u == null || !u.directory) SectionCard(stringResource(if (u == null) R.string.usr_password else R.string.usr_reset_password)) {
                    if (self) Text(stringResource(R.string.usr_own_password), style = MaterialTheme.typography.bodySmall)
                    else {
                        Field(stringResource(R.string.usr_new_password), pass, { pass = it }, secret = true, error = if (pass.isNotEmpty()) passwordError(pass) else null, help = stringResource(R.string.usr_pass_rules))
                        Field(stringResource(R.string.usr_confirm_password), pass2, { pass2 = it }, secret = true, error = if (pass2.isNotEmpty() && pass != pass2) stringResource(R.string.usr_pass_mismatch) else null)
                        if (u != null) {
                            Text(stringResource(R.string.usr_reset_signs_out), style = MaterialTheme.typography.bodySmall)
                            Button(enabled = passOk && !actions.busy, onClick = {
                                actions.run(done = res.getString(R.string.usr_password_changed), after = { pass = ""; pass2 = "" }) { it.updateUser(u.username, update(pass)) }
                            }) { Text(stringResource(R.string.usr_reset_password)) }
                        }
                    }
                }
                if (u == null) Button(modifier = Modifier.fillMaxWidth(), enabled = userNameRe.matches(username) && passOk && !actions.busy, onClick = {
                    actions.run(after = onBack) { it.createUser(UserCreate(username, pass, fullName, email, role, groups.sorted(), ssh, docker)) }
                }) { Text(stringResource(R.string.usr_create)) }
                if (u != null) {
                    SectionCard(stringResource(R.string.usr_security)) {
                        KeyValue(stringResource(R.string.usr_2fa), stringResource(if (u.twoFactor) R.string.usr_on else R.string.usr_off))
                        KeyValue(stringResource(R.string.usr_passkeys), u.passkeys.toString())
                        if (u.twoFactor && !self) OutlinedButton(onClick = { reset2fa = true }) { Text(stringResource(R.string.usr_reset_2fa)) }
                        if (self) Text(stringResource(R.string.usr_own_security), style = MaterialTheme.typography.bodySmall)
                    }
                    Quotas(u)
                    SharePermissions(u)
                }
            }
        }
    }
    if (deleting && u != null) Confirm(stringResource(R.string.usr_delete), stringResource(R.string.usr_delete_text, u.username), stringResource(R.string.b_delete),
        onConfirm = { actions.run(after = onBack) { it.deleteUser(u.username, removeHome) } }, onDismiss = { deleting = false }, typeToConfirm = u.username,
        extra = {
            if (!u.directory) Row(verticalAlignment = Alignment.CenterVertically) {
                Checkbox(removeHome, { removeHome = it })
                Text(stringResource(R.string.usr_remove_home))
            }
        })
    if (reset2fa && u != null) Confirm(stringResource(R.string.usr_reset_2fa), stringResource(R.string.usr_reset_2fa_text, u.username), stringResource(R.string.usr_reset_2fa),
        onConfirm = { actions.run(done = res.getString(R.string.b_saved), after = onBack) { it.reset2fa(u.username) } }, onDismiss = { reset2fa = false })
}

@Composable
private fun Quotas(u: NasUser) {
    val data = rememberData(u.username) { it.userQuotas(u.username) }
    val actions = rememberActions()
    val res = LocalResources.current
    SectionCard(stringResource(R.string.usr_quotas)) {
        val list = data.value
        if (list == null) Text(stringResource(R.string.usr_loading), style = MaterialTheme.typography.bodySmall)
        else if (list.isEmpty()) Text(stringResource(R.string.usr_no_quota_target), style = MaterialTheme.typography.bodySmall)
        list?.forEach { q ->
            var gb by remember(q.limit) { mutableStateOf(if (q.limit > 0) "%.0f".format(q.limit / 1e9) else "") }
            Column {
                Text(q.name, style = MaterialTheme.typography.bodyLarge)
                Text(stringResource(R.string.usr_quota_used, formatBytes(q.used), if (q.limit > 0) formatBytes(q.limit) else stringResource(R.string.cam_no_limit)), style = MaterialTheme.typography.bodySmall)
                if (!q.supported || !q.enabled) Text(stringResource(R.string.usr_quota_off), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                else Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Field(stringResource(R.string.usr_quota_gb), gb, { gb = it.filter { c -> c.isDigit() || c == '.' } }, Modifier.weight(1f), number = true, help = stringResource(R.string.cam_zero_no_limit))
                    TextButton(enabled = !actions.busy, onClick = {
                        actions.run(done = res.getString(R.string.b_saved), after = data.reload) { it.setUserQuota(u.username, q.kind, q.target, ((gb.toDoubleOrNull() ?: 0.0) * 1e9).toLong()) }
                    }) { Text(stringResource(R.string.b_save)) }
                }
            }
        }
    }
}

@Composable
private fun SharePermissions(u: NasUser) {
    val data = rememberData(u.username) { it.shares() }
    val actions = rememberActions()
    var tick by remember { mutableIntStateOf(0) }
    SectionCard(stringResource(R.string.usr_share_access)) {
        Text(stringResource(R.string.usr_share_help), style = MaterialTheme.typography.bodySmall)
        data.value?.forEach { s ->
            val cur = s.permissions[u.username] ?: ""
            Choice(s.name, listOf("" to stringResource(R.string.usr_perm_default), "rw" to stringResource(R.string.usr_perm_rw), "ro" to stringResource(R.string.usr_perm_ro), "none" to stringResource(R.string.usr_perm_none)),
                cur, { p -> if (p != cur) actions.run(after = { data.reload(); tick++ }) { it.setSharePermission(s.name, u.username, p) } }, enabled = !actions.busy)
        }
        if (data.value?.isEmpty() == true) Text(stringResource(R.string.usr_no_shares), style = MaterialTheme.typography.bodySmall)
    }
}
