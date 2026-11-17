package io.github.nathplay15.strataos.ui.assistant

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.AutoAwesome
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.History
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.AssistantAction
import io.github.nathplay15.strataos.net.AssistantConversation
import io.github.nathplay15.strataos.net.AssistantMsg
import io.github.nathplay15.strataos.net.assistantChat
import io.github.nathplay15.strataos.net.assistantConfirm
import io.github.nathplay15.strataos.net.assistantConversation
import io.github.nathplay15.strataos.net.assistantConversations
import io.github.nathplay15.strataos.net.assistantConfig
import io.github.nathplay15.strataos.net.deleteAssistantConversation
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.Loaded
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.errorText
import io.github.nathplay15.strataos.ui.formatDate
import io.github.nathplay15.strataos.ui.rememberLoad
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * The assistant as a tab of the app: the same conversations as the web
 * interface (the NAS keeps them per user), the tools of the NAS and the
 * confirmation of the write actions before they run.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AssistantScreen(accountAction: @Composable () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val cfg = rememberLoad(Unit) { api().assistantConfig() }
    var convId by rememberSaveable { mutableStateOf("") }
    var msgs by remember { mutableStateOf(listOf<AssistantMsg>()) }
    var input by rememberSaveable { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var pending by remember { mutableStateOf<AssistantAction?>(null) }
    var history by remember { mutableStateOf(false) }

    fun open(id: String, m: List<AssistantMsg>) {
        convId = id
        msgs = m
        pending = null
    }

    fun send(text: String) {
        val t = text.trim()
        if (t.isEmpty() || busy) return
        input = ""
        val sent = msgs + AssistantMsg("user", t)
        msgs = sent
        busy = true
        scope.launch {
            try {
                val r = withContext(Dispatchers.IO) { api().assistantChat(sent, convId) }
                convId = r.conversationId
                msgs = msgs + AssistantMsg("assistant", r.reply.ifEmpty { "…" })
                pending = r.actions.firstOrNull()
            } catch (e: Exception) {
                msgs = msgs + AssistantMsg("assistant", errorText(ctx, e))
            } finally {
                busy = false
            }
        }
    }

    Column(Modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text(stringResource(R.string.a_assistant)) },
            actions = {
                IconButton(onClick = { open("", emptyList()) }) { Icon(Icons.Default.Add, stringResource(R.string.a_assistant_new)) }
                IconButton(onClick = { history = true }) { Icon(Icons.Default.History, stringResource(R.string.a_assistant_history)) }
                accountAction()
            },
        )
        when (val c = cfg.state) {
            is Loaded.Ok -> if (!c.value.enabled) {
                EmptyState(Icons.Default.AutoAwesome, stringResource(R.string.a_assistant_disabled))
            } else {
                val list = rememberLazyListState()
                LaunchedEffect(msgs.size) { if (msgs.isNotEmpty()) list.animateScrollToItem(msgs.size - 1) }
                Column(Modifier.fillMaxSize().imePadding()) {
                    LazyColumn(Modifier.weight(1f), state = list, contentPadding = PaddingValues(16.dp, 8.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                        if (msgs.isEmpty()) {
                            item { Text(stringResource(R.string.a_assistant_empty), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        }
                        items(msgs) { m -> Bubble(m) }
                        if (busy) item { Text(stringResource(R.string.a_assistant_thinking), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        pending?.let { a -> item {
                            ActionCard(a, onConfirm = {
                                busy = true
                                scope.launch {
                                    try {
                                        val r = withContext(Dispatchers.IO) { api().assistantConfirm(a.token) }
                                        msgs = msgs + AssistantMsg("assistant", a.summary + "\n\n" + r.result)
                                    } catch (e: Exception) {
                                        msgs = msgs + AssistantMsg("assistant", errorText(ctx, e))
                                    } finally {
                                        pending = null
                                        busy = false
                                    }
                                }
                            }, onDismiss = { pending = null })
                        } }
                    }
                    Surface(tonalElevation = 2.dp) {
                        Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.Bottom) {
                            OutlinedTextField(input, { input = it }, placeholder = { Text(stringResource(R.string.a_assistant_hint)) },
                                modifier = Modifier.weight(1f), maxLines = 4, enabled = !busy,
                                shape = RoundedCornerShape(22.dp))
                            IconButton(onClick = { send(input) }, enabled = !busy && input.isNotBlank()) {
                                Icon(Icons.AutoMirrored.Filled.Send, stringResource(R.string.a_assistant_confirm))
                            }
                        }
                    }
                }
            }
            else -> Refreshable(cfg) {}
        }
    }
    if (history) {
        HistoryDialog(onDismiss = { history = false }, onOpen = { c ->
            history = false
            scope.launch {
                try {
                    val chat = withContext(Dispatchers.IO) { api().assistantConversation(c.id) }
                    open(chat.id, chat.messages)
                } catch (e: Exception) {
                    open(c.id, listOf(AssistantMsg("assistant", errorText(ctx, e))))
                }
            }
        })
    }
}

@Composable
private fun Bubble(m: AssistantMsg) {
    val user = m.role == "user"
    Row(Modifier.fillMaxWidth(), horizontalArrangement = if (user) Arrangement.End else Arrangement.Start) {
        Surface(
            shape = RoundedCornerShape(16.dp),
            color = if (user) MaterialTheme.colorScheme.primaryContainer else MaterialTheme.colorScheme.surfaceVariant,
            modifier = Modifier.widthIn(max = 320.dp),
        ) {
            Text(m.content, Modifier.padding(12.dp), style = MaterialTheme.typography.bodyMedium)
        }
    }
}

@Composable
private fun ActionCard(a: AssistantAction, onConfirm: () -> Unit, onDismiss: () -> Unit) {
    Surface(shape = RoundedCornerShape(12.dp), color = MaterialTheme.colorScheme.tertiaryContainer, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(stringResource(R.string.a_assistant_asks), style = MaterialTheme.typography.labelMedium)
            Text(a.summary, style = MaterialTheme.typography.bodyMedium)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = onConfirm) { Text(stringResource(R.string.a_assistant_confirm)) }
                TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) }
            }
        }
    }
}

@Composable
private fun HistoryDialog(onDismiss: () -> Unit, onOpen: (AssistantConversation) -> Unit) {
    val scope = rememberCoroutineScope()
    var tick by remember { mutableIntStateOf(0) }
    val load = rememberLoad(tick) { api().assistantConversations() }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.a_assistant_history)) },
        text = {
            when (val s = load.state) {
                is Loaded.Ok -> if (s.value.isEmpty()) {
                    Text(stringResource(R.string.a_assistant_no_history), color = MaterialTheme.colorScheme.onSurfaceVariant)
                } else {
                    Column(Modifier.heightIn(max = 420.dp)) {
                        s.value.forEach { c ->
                            ListItem(
                                headlineContent = { Text(c.title, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                                supportingContent = { Text(formatDate(c.updated), style = MaterialTheme.typography.labelSmall) },
                                trailingContent = {
                                    IconButton(onClick = {
                                        scope.launch {
                                            withContext(Dispatchers.IO) { runCatching { api().deleteAssistantConversation(c.id) } }
                                            tick++
                                        }
                                    }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete)) }
                                },
                                modifier = Modifier.clickable { onOpen(c) },
                            )
                        }
                    }
                }
                else -> Box(Modifier.fillMaxWidth().padding(24.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.a_assistant_done)) } },
    )
}
