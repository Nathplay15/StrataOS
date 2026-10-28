package io.github.nathplay15.strataos.ui.apps

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.background
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Apps
import androidx.compose.material.icons.filled.SystemUpdateAlt
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.AppCatalog
import io.github.nathplay15.strataos.net.CatalogApp
import io.github.nathplay15.strataos.net.DockerUpdates
import io.github.nathplay15.strataos.net.appCatalog
import io.github.nathplay15.strataos.net.checkUpdates
import io.github.nathplay15.strataos.net.dockerInfo
import io.github.nathplay15.strataos.net.dockerUpdates
import io.github.nathplay15.strataos.net.installApp
import io.github.nathplay15.strataos.net.uninstallApp
import io.github.nathplay15.strataos.net.uninstallPlan
import io.github.nathplay15.strataos.net.updateAll
import io.github.nathplay15.strataos.net.updateStack
import io.github.nathplay15.strataos.ui.Actions
import io.github.nathplay15.strataos.ui.Chip
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.Load
import io.github.nathplay15.strataos.ui.Loaded
import io.github.nathplay15.strataos.ui.RefreshAction
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.SectionScaffold
import io.github.nathplay15.strataos.ui.Tone
import io.github.nathplay15.strataos.ui.color
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.formatDate
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import io.github.nathplay15.strataos.ui.storage.FullDialog
import io.github.nathplay15.strataos.ui.toast
import kotlinx.coroutines.delay

/** Address of the web page of an app on the NAS (same host as the API). */
fun appUrl(port: String): String? {
    val host = Graph.prefs.server?.url?.let { Uri.parse(it).host } ?: return null
    if (port.toIntOrNull() == null) return null
    return "http://" + (if (host.contains(':')) "[$host]" else host) + ":" + port
}

/** Applications (administrators): App Center, containers, stacks, images, one-click updates. */
@Composable
fun AppsSection(back: () -> Unit) {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    val info = rememberLoad(Unit) { api().dockerInfo() }
    var tick by remember { mutableIntStateOf(0) }
    val updates = rememberLoad(tick) { runCatching { api().dockerUpdates() }.getOrDefault(DockerUpdates()) }
    // The check runs on the NAS: follow it while it runs.
    LaunchedEffect(updates.value?.running) { if (updates.value?.running == true) { delay(3000); tick++ } }
    val tabs = listOf(R.string.a_app_center, R.string.a_containers, R.string.a_stacks, R.string.a_images).map { stringResource(it) }
    SectionScaffold(stringResource(R.string.a_applications), back, actions = { RefreshAction { info.reload(); tick++ } }, tabs = tabs, tab = tab, onTab = { tab = it }) { m ->
        Box(m) {
            when (val s = info.state) {
                is Loaded.Ok -> if (!s.value.available) EmptyState(Icons.Default.Apps, stringResource(R.string.a_docker_missing, s.value.error))
                else {
                    val u = updates.value ?: DockerUpdates()
                    val refreshUpdates: () -> Unit = { tick++ }
                    when (tab) {
                        0 -> AppCenter(u, refreshUpdates)
                        1 -> ContainersTab(u, refreshUpdates)
                        2 -> StacksTab(u, refreshUpdates)
                        else -> ImagesTab()
                    }
                }
                else -> Refreshable(info) {}
            }
        }
    }
}

/** Last check, "Check now" and "Update all" (above the lists). */
@Composable
fun UpdatesBar(u: DockerUpdates, act: Actions, refresh: () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp)) {
        Text(
            when {
                u.running -> stringResource(R.string.a_updates_checking)
                u.checked.isEmpty() -> stringResource(R.string.a_updates_never)
                else -> stringResource(R.string.a_updates_checked, formatDate(u.checked))
            } + if (u.available > 0) " · " + stringResource(R.string.a_updates_n, u.available) else "",
            style = MaterialTheme.typography.bodySmall, color = if (u.available > 0) Tone.WARN.color() else MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { act.job(refresh) { api().checkUpdates() } }, enabled = !u.running && !act.busy.value) { Text(stringResource(R.string.a_check_updates)) }
            if (u.available > 0) Button(onClick = { act.job(refresh) { api().updateAll() } }, enabled = !act.busy.value) { Text(stringResource(R.string.a_update_all)) }
        }
    }
}

@Composable
fun UpdateChip() = Chip(stringResource(R.string.a_update_available), Tone.WARN)

/** Filters of the catalog: "" = all, "*" = installed, else a category. */
fun filterApps(c: AppCatalog, filter: String, q: String): List<CatalogApp> {
    val query = q.trim().lowercase()
    return c.apps.filter { a ->
        (filter.isEmpty() || (filter == "*" && c.installed.containsKey(a.id)) || a.category == filter) &&
            (query.isEmpty() || (a.name + " " + a.description + " " + a.category).lowercase().contains(query))
    }
}

@Composable
private fun AppCenter(u: DockerUpdates, refreshUpdates: () -> Unit) {
    val load = rememberLoad(Unit) { api().appCatalog() }
    val act = rememberActions()
    var q by rememberSaveable { mutableStateOf("") }
    var filter by rememberSaveable { mutableStateOf("") }
    var open by remember { mutableStateOf<CatalogApp?>(null) }
    Refreshable(load) { c ->
        val cats = c.apps.map { it.category }.distinct().sorted()
        LazyColumn(Modifier.fillMaxSize()) {
            item { UpdatesBar(u, act) { refreshUpdates(); load.reload() } }
            item {
                OutlinedTextField(q, { q = it }, placeholder = { Text(stringResource(R.string.a_search_apps)) }, singleLine = true, modifier = Modifier.fillMaxWidth().padding(12.dp, 4.dp))
                Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp)) {
                    (listOf("" to stringResource(R.string.a_all), "*" to stringResource(R.string.a_installed)) + cats.map { it to it }).forEach { (id, label) ->
                        FilterChip(filter == id, { filter = id }, { Text(label) }, Modifier.padding(4.dp))
                    }
                }
            }
            val custom = if (filter.isEmpty() || filter == "*") c.custom.filter { q.isBlank() || it.name.contains(q, ignoreCase = true) } else emptyList()
            items(custom, key = { "c" + it.stack }) { a ->
                ListItem(
                    headlineContent = { Text(a.name) },
                    supportingContent = { Text(a.description.ifEmpty { stringResource(R.string.a_custom_app, a.stack) }, maxLines = 2) },
                    leadingContent = { Letter(a.name) },
                    trailingContent = { if (u.stackHasUpdate(a.stack)) UpdateChip() else Chip(stringResource(R.string.a_installed), Tone.OK) },
                    modifier = Modifier.clickable { open = CatalogApp(id = "", name = a.name, description = a.description, webPort = a.webPort.toString()).copy(id = "custom:" + a.stack) },
                )
                HorizontalDivider()
            }
            val apps = filterApps(c, filter, q)
            if (apps.isEmpty() && custom.isEmpty()) item { Text(stringResource(R.string.a_no_results), Modifier.padding(24.dp), color = MaterialTheme.colorScheme.onSurfaceVariant) }
            items(apps, key = { it.id }) { a ->
                val stack = c.installed[a.id]
                ListItem(
                    headlineContent = { Text(a.name) },
                    supportingContent = { Text(a.description, maxLines = 2, overflow = TextOverflow.Ellipsis) },
                    overlineContent = { Text(a.category) },
                    leadingContent = { Letter(a.name) },
                    trailingContent = {
                        when {
                            stack != null && u.stackHasUpdate(stack) -> UpdateChip()
                            stack != null -> Chip(stringResource(R.string.a_installed), Tone.OK)
                        }
                    },
                    modifier = Modifier.clickable { open = a },
                )
                HorizontalDivider()
            }
        }
        open?.let { a ->
            val customStack = a.id.removePrefix("custom:").takeIf { a.id.startsWith("custom:") }
            AppDetail(a, c, customStack ?: c.installed[a.id], u, onDismiss = { open = null }) { load.reload(); refreshUpdates() }
        }
    }
}

@Composable
private fun Letter(name: String) {
    Box(Modifier.size(40.dp).background(MaterialTheme.colorScheme.primaryContainer, CircleShape), contentAlignment = Alignment.Center) {
        Text(name.take(1).uppercase(), color = MaterialTheme.colorScheme.onPrimaryContainer, style = MaterialTheme.typography.titleMedium)
    }
}

/** An app: install it with its settings, or open, update and uninstall it. */
@Composable
private fun AppDetail(a: CatalogApp, c: AppCatalog, stack: String?, u: DockerUpdates, onDismiss: () -> Unit, changed: () -> Unit) {
    val ctx = LocalContext.current
    val act = rememberActions()
    var uninstalling by remember { mutableStateOf(false) }
    val linked = c.linked[a.id].orEmpty()
    var name by remember { mutableStateOf(a.id) }
    val values = remember { mutableStateOf(a.vars.associate { it.key to (linked[it.key]?.takeIf { v -> v.isNotEmpty() } ?: it.default) }) }
    FullDialog(a.name, onDismiss) {
        Text(a.description)
        if (stack != null) {
            Text(stringResource(R.string.a_installed_as, stack), style = MaterialTheme.typography.bodySmall)
            val port = c.ports[a.id] ?: a.webPort.takeIf { it.toIntOrNull() != null } ?: ""
            appUrl(port)?.let { url ->
                Button(onClick = { runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) } }, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.a_open_app, url)) }
            }
            if (u.stackHasUpdate(stack)) Card { Text(stringResource(R.string.a_update_ready), Modifier.padding(12.dp)) }
            OutlinedButton(onClick = { act.job(changed) { api().updateStack(stack) } }, modifier = Modifier.fillMaxWidth()) {
                Icon(Icons.Default.SystemUpdateAlt, null)
                Text(stringResource(if (u.stackHasUpdate(stack)) R.string.a_update_now else R.string.a_update_pull), Modifier.padding(start = 8.dp))
            }
            Button(onClick = { uninstalling = true }, colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error), modifier = Modifier.fillMaxWidth()) {
                Text(stringResource(R.string.a_uninstall))
            }
        } else if (a.id.isNotEmpty()) {
            OutlinedTextField(name, { name = it.trim().lowercase() }, label = { Text(stringResource(R.string.a_stack_name)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
            a.vars.forEach { v ->
                val value = values.value[v.key] ?: ""
                OutlinedTextField(value, { values.value = values.value + (v.key to it) }, label = { Text(v.label.ifEmpty { v.key }) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
                    visualTransformation = if (v.type == "password") PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
                    supportingText = {
                        Text(when {
                            v.key in linked -> stringResource(R.string.a_var_linked)
                            v.type == "password" -> stringResource(if (v.length > 0) R.string.a_var_password_len else R.string.a_var_password, v.length)
                            v.type == "path" -> stringResource(R.string.a_var_path)
                            v.type == "port" -> stringResource(R.string.a_var_port)
                            else -> ""
                        })
                    })
            }
            Button(onClick = { onDismiss(); act.job(changed) { api().installApp(a.id, name, values.value.filterValues { it.isNotEmpty() }) } },
                enabled = Regex("^[a-z0-9][a-z0-9_-]{0,62}$").matches(name), modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.a_install)) }
        }
    }
    if (uninstalling && stack != null) UninstallDialog(stack, a.name, act, onDismiss = { uninstalling = false }) { onDismiss(); changed() }
}

@Composable
private fun UninstallDialog(stack: String, name: String, act: Actions, onDismiss: () -> Unit, done: () -> Unit) {
    val ctx = LocalContext.current
    val res = androidx.compose.ui.platform.LocalResources.current
    val plan: Load<io.github.nathplay15.strataos.net.AppPlan> = rememberLoad(stack) { api().uninstallPlan(stack) }
    var data by remember { mutableStateOf(false) }
    var images by remember { mutableStateOf(true) }
    val p = plan.value
    ConfirmDialog(
        title = stringResource(R.string.a_uninstall_title, name),
        text = stringResource(if (data) R.string.a_uninstall_with_data else R.string.a_uninstall_keep_data),
        confirm = stringResource(R.string.a_uninstall), danger = true,
        typeToConfirm = if (data) stack else null,
        details = if (p == null) listOf("…") else (if (data) p.volumes.orEmpty().map { "volume $it" } + p.folders.orEmpty() else emptyList()) + p.images.orEmpty().takeIf { images }.orEmpty().map { "image $it" },
        onDismiss = onDismiss,
        onConfirm = { act.job({ toast(ctx, res.getString(R.string.a_job_done)); done() }) { api().uninstallApp(stack, data, images) } },
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) { Text(stringResource(R.string.a_delete_app_data), Modifier.weight(1f)); Switch(data, { data = it }) }
        Row(verticalAlignment = Alignment.CenterVertically) { Text(stringResource(R.string.a_remove_images), Modifier.weight(1f)); Switch(images, { images = it }) }
        if (plan.state is Loaded.Failed) TextButton(onClick = plan.reload) { Text(stringResource(R.string.retry)) }
    }
}
