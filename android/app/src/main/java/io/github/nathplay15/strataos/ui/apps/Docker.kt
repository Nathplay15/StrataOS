package io.github.nathplay15.strataos.ui.apps

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Inventory2
import androidx.compose.material.icons.filled.ViewInAr
import androidx.compose.material.icons.filled.Widgets
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Container
import io.github.nathplay15.strataos.net.ContainerStats
import io.github.nathplay15.strataos.net.DockerImage
import io.github.nathplay15.strataos.net.DockerUpdates
import io.github.nathplay15.strataos.net.Stack
import io.github.nathplay15.strataos.net.containerAction
import io.github.nathplay15.strataos.net.containerStats
import io.github.nathplay15.strataos.net.containers
import io.github.nathplay15.strataos.net.deleteContainer
import io.github.nathplay15.strataos.net.deleteImage
import io.github.nathplay15.strataos.net.deleteStack
import io.github.nathplay15.strataos.net.followLogs
import io.github.nathplay15.strataos.net.images
import io.github.nathplay15.strataos.net.json
import io.github.nathplay15.strataos.net.pullImage
import io.github.nathplay15.strataos.net.stackAction
import io.github.nathplay15.strataos.net.stackLogs
import io.github.nathplay15.strataos.net.stacks
import io.github.nathplay15.strataos.net.updateContainer
import io.github.nathplay15.strataos.net.updateStack
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.InfoRow
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.TextDialog
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatTime
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import io.github.nathplay15.strataos.ui.storage.CardList
import io.github.nathplay15.strataos.ui.storage.FullDialog
import io.github.nathplay15.strataos.ui.storage.ItemCard
import kotlinx.coroutines.delay
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener

@Composable
fun ContainersTab(u: DockerUpdates, refreshUpdates: () -> Unit) {
    val load = rememberLoad(Unit) { api().containers().sortedBy { it.name } }
    var stats by remember { mutableStateOf<Map<String, ContainerStats>>(emptyMap()) }
    LaunchedEffect(Unit) {
        while (true) {
            stats = runCatching { kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { api().containerStats() } }.getOrDefault(stats)
            delay(5000)
        }
    }
    val act = rememberActions()
    var open by remember { mutableStateOf<Container?>(null) }
    Refreshable(load) { list ->
        CardList {
            item { UpdatesBar(u, act) { refreshUpdates(); load.reload() } }
            if (list.isEmpty()) item { EmptyState(Icons.Default.ViewInAr, stringResource(R.string.a_no_containers), Modifier.heightIn(max = 300.dp)) }
            items(list, key = { it.id }) { c ->
                val st = stats[c.id]
                ItemCard(c.name, c.state, listOf(c.image, c.stack.takeIf { it.isNotEmpty() }?.let { "stack $it" }).filterNotNull().joinToString(" · "), onClick = { open = c }) {
                    if (u.hasUpdate(c)) UpdateChip()
                    Text(c.status + if (st != null) " · CPU %.1f %% · %s".format(st.cpuPercent, formatBytes(st.memUsage)) else "", style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
    open?.let { c -> ContainerDetail(c, stats[c.id], u, onDismiss = { open = null }) { load.reload(); refreshUpdates() } }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ContainerDetail(c: Container, st: ContainerStats?, u: DockerUpdates, onDismiss: () -> Unit, changed: () -> Unit) {
    val act = rememberActions()
    var logs by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    fun action(a: String) = act.run(changed) { api().containerAction(c.id, a) }
    FullDialog(c.name, onDismiss) {
        InfoRow(stringResource(R.string.a_image), c.image)
        InfoRow(stringResource(R.string.a_state), c.state + " — " + c.status)
        if (c.stack.isNotEmpty()) InfoRow(stringResource(R.string.a_stack), c.stack)
        c.ports.orEmpty().filter { it.publicPort > 0 }.distinctBy { it.publicPort to it.type }.takeIf { it.isNotEmpty() }?.let { ports ->
            InfoRow(stringResource(R.string.a_ports), ports.joinToString(", ") { "${it.publicPort} → ${it.privatePort}/${it.type}" })
        }
        if (st != null) {
            InfoRow("CPU", "%.1f %%".format(st.cpuPercent))
            InfoRow(stringResource(R.string.a_memory), formatBytes(st.memUsage) + if (st.memLimit > 0) " / " + formatBytes(st.memLimit) else "")
            InfoRow(stringResource(R.string.a_network), "↓ ${formatBytes(st.netRx)} ↑ ${formatBytes(st.netTx)}")
            InfoRow(stringResource(R.string.a_disk_io), "R ${formatBytes(st.blockRead)} W ${formatBytes(st.blockWrite)}")
        }
        val update = u.container(c.id)
        when (update?.status) {
            "available" -> Button(onClick = { onDismiss(); act.job(changed) { api().updateContainer(c.id) } }, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.a_update_now)) }
            "error" -> Text(stringResource(R.string.a_update_check_failed, update.error), style = MaterialTheme.typography.bodySmall)
            "unsupported" -> Text(stringResource(R.string.a_update_unsupported), style = MaterialTheme.typography.bodySmall)
        }
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            when (c.state) {
                "running" -> {
                    OutlinedButton(onClick = { action("stop") }) { Text(stringResource(R.string.a_stop)) }
                    OutlinedButton(onClick = { action("restart") }) { Text(stringResource(R.string.a_restart)) }
                    OutlinedButton(onClick = { action("pause") }) { Text(stringResource(R.string.a_pause)) }
                }
                "paused" -> OutlinedButton(onClick = { action("unpause") }) { Text(stringResource(R.string.a_resume)) }
                else -> OutlinedButton(onClick = { action("start") }) { Text(stringResource(R.string.a_start)) }
            }
            OutlinedButton(onClick = { logs = true }) { Text(stringResource(R.string.a_logs)) }
            OutlinedButton(onClick = { deleting = true }) { Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }
        }
    }
    if (logs) LogsDialog(c.name, { logs = false }) { lines -> LiveLogs(c.id, lines) }
    if (deleting) {
        var volumes by remember { mutableStateOf(false) }
        ConfirmDialog(stringResource(R.string.a_delete_container, c.name),
            stringResource(if (c.stack.isNotEmpty()) R.string.a_delete_container_stack else R.string.a_delete_container_text, c.stack), stringResource(R.string.a_delete), danger = true,
            onDismiss = { deleting = false }, onConfirm = { onDismiss(); act.run(changed) { api().deleteContainer(c.id, volumes) } }) {
            Row(verticalAlignment = Alignment.CenterVertically) { Text(stringResource(R.string.a_delete_anon_volumes), Modifier.weight(1f)); Switch(volumes, { volumes = it }) }
        }
    }
}

/** Follows the logs of a container over the WebSocket of the NAS into [lines]. */
@Composable
private fun LiveLogs(id: String, lines: MutableList<String>) {
    DisposableEffect(id) {
        val main = android.os.Handler(android.os.Looper.getMainLooper())
        val ws: WebSocket = api().followLogs(id, object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) {
                val line = runCatching { (json.parseToJsonElement(text) as JsonObject)["l"].let { (it as JsonPrimitive).content } }.getOrDefault(text)
                main.post { lines.add(line); if (lines.size > 1500) lines.removeAt(0) }
            }
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                main.post { lines.add("— " + (t.message ?: "connection lost")) }
            }
        })
        onDispose { ws.close(1000, null) }
    }
}

@Composable
private fun LogsDialog(title: String, onDismiss: () -> Unit, source: @Composable (MutableList<String>) -> Unit) {
    val lines = remember { mutableStateListOf<String>() }
    source(lines)
    val state = rememberLazyListState()
    LaunchedEffect(lines.size) { if (lines.isNotEmpty()) state.scrollToItem(lines.size - 1) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.a_logs_of, title)) },
        text = {
            Box(Modifier.fillMaxWidth().heightIn(min = 200.dp, max = 520.dp).background(Color(0xFF0B0F1C)).padding(6.dp)) {
                if (lines.isEmpty()) Text(stringResource(R.string.a_waiting_logs), color = Color(0xFF8A93AD), fontSize = 12.sp)
                LazyColumn(state = state) {
                    items(lines.size) { i -> Text(lines[i], color = Color(0xFFCBD3E6), fontFamily = FontFamily.Monospace, fontSize = 11.sp) }
                }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.a_close)) } },
    )
}

@Composable
fun StacksTab(u: DockerUpdates, refreshUpdates: () -> Unit) {
    val load = rememberLoad(Unit) { api().stacks() }
    val act = rememberActions()
    var logsOf by remember { mutableStateOf<String?>(null) }
    var deleting by remember { mutableStateOf<Stack?>(null) }
    val changed = { load.reload(); refreshUpdates() }
    Refreshable(load) { list ->
        CardList {
            item { UpdatesBar(u, act, changed) }
            if (list.isEmpty()) item { EmptyState(Icons.Default.Widgets, stringResource(R.string.a_no_stacks), Modifier.heightIn(max = 300.dp)) }
            items(list, key = { it.name }) { s ->
                ItemCard(s.name, s.status, stringResource(R.string.a_stack_counts, s.running, s.services) + (if (s.app.isNotEmpty()) " · App Center: ${s.app}" else ""),
                    menu = { close ->
                        if (s.status == "stopped") DropdownMenuItem({ Text(stringResource(R.string.a_start)) }, { close(); act.job(changed) { api().stackAction(s.name, "up") } })
                        else {
                            DropdownMenuItem({ Text(stringResource(R.string.a_stop)) }, { close(); act.job(changed) { api().stackAction(s.name, "stop") } })
                            DropdownMenuItem({ Text(stringResource(R.string.a_restart)) }, { close(); act.job(changed) { api().stackAction(s.name, "restart") } })
                        }
                        DropdownMenuItem({ Text(stringResource(if (u.stackHasUpdate(s.name)) R.string.a_update_now else R.string.a_update_pull)) }, { close(); act.job(changed) { api().updateStack(s.name) } })
                        DropdownMenuItem({ Text(stringResource(R.string.a_logs)) }, { close(); logsOf = s.name })
                        DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, { close(); deleting = s })
                    }) {
                    if (u.stackHasUpdate(s.name)) UpdateChip()
                    if (s.modifiedAt.isNotEmpty()) Text(stringResource(R.string.a_modified, io.github.nathplay15.strataos.ui.formatDate(s.modifiedAt)), style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
    logsOf?.let { name ->
        LogsDialog(name, { logsOf = null }) { lines ->
            LaunchedEffect(name) {
                val text = runCatching { kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { api().stackLogs(name) } }.getOrElse { it.message ?: "" }
                lines.clear(); lines.addAll(text.lines().takeLast(1500))
            }
        }
    }
    deleting?.let { s ->
        ConfirmDialog(stringResource(R.string.a_delete_stack, s.name), stringResource(R.string.a_delete_stack_text), stringResource(R.string.a_delete), danger = true, typeToConfirm = s.name,
            onDismiss = { deleting = null }, onConfirm = { act.run(changed) { api().deleteStack(s.name, true) } })
    }
}

@Composable
fun ImagesTab() {
    val load = rememberLoad(Unit) { api().images().sortedBy { it.title } }
    val act = rememberActions()
    var pulling by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf<DockerImage?>(null) }
    Refreshable(load) { list ->
        CardList {
            item { Button(onClick = { pulling = true }) { Text(stringResource(R.string.a_pull_image)) } }
            if (list.isEmpty()) item { EmptyState(Icons.Default.Inventory2, stringResource(R.string.a_no_images), Modifier.heightIn(max = 300.dp)) }
            items(list, key = { it.id }) { img ->
                ItemCard(img.title, if (img.containers == 0) "unused" else "", listOf(formatBytes(img.size), formatTime(img.created * 1000)).joinToString(" · "),
                    menu = { close -> DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, { close(); deleting = img }) }) {
                    val more = img.repoTags.orEmpty().drop(1)
                    if (more.isNotEmpty()) Text(more.joinToString(", "), style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
    if (pulling) TextDialog(stringResource(R.string.a_pull_image), stringResource(R.string.a_image_ref), confirm = stringResource(R.string.a_pull),
        valid = { it.isNotBlank() && !it.contains(' ') }, onDismiss = { pulling = false }) { ref -> act.job(load.reload) { api().pullImage(ref) } }
    deleting?.let { img ->
        ConfirmDialog(stringResource(R.string.a_delete_image, img.title), stringResource(R.string.a_delete_image_text), stringResource(R.string.a_delete), danger = true,
            onDismiss = { deleting = null }, onConfirm = { act.run(load.reload) { api().deleteImage(img.id) } })
    }
}
