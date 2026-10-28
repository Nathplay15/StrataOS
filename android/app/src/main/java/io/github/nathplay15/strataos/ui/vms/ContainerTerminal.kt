package io.github.nathplay15.strataos.ui.vms

import android.os.Handler
import android.os.Looper
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Keyboard
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.manage.lxcConsoleUrl
import io.github.nathplay15.strataos.term.TermKeys
import io.github.nathplay15.strataos.term.TermScreen
import io.github.nathplay15.strataos.term.TerminalView
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.errorText
import java.util.concurrent.TimeUnit
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString

/**
 * The terminal of a container over the websocket of the NAS
 * (GET /api/lxc/{name}/console): binary output, JSON input and resize
 * messages. "shell" is root's shell; "console" the container's console with
 * its login prompt.
 */
class TermSession(api: Api, name: String, mode: String, private val screen: TermScreen, private val onOutput: () -> Unit, private val onEnd: (String?) -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    @Volatile private var closed = false
    private val ws: WebSocket

    init {
        val client = api.http.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).pingInterval(20, TimeUnit.SECONDS).build()
        ws = client.newWebSocket(api.request(api.lxcConsoleUrl(name, mode)).build(), object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                val b = bytes.toByteArray()
                main.post { screen.feed(b); onOutput() }
            }
            override fun onMessage(webSocket: WebSocket, text: String) {
                main.post { screen.feed(text); onOutput() }
            }
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
                end(null)
            }
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = end(null)
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                end(if (response != null && !response.isSuccessful) Api.apiError(response.code, runCatching { response.body.string() }.getOrDefault("")).message else t.message ?: "")
            }
        })
    }

    private fun end(err: String?) {
        if (closed) return
        closed = true
        main.post { onEnd(err) }
    }

    fun input(s: String) {
        if (!closed) ws.send(buildJsonObject { put("type", "input"); put("data", s) }.toString())
    }

    fun resize(cols: Int, rows: Int) {
        if (!closed) ws.send(buildJsonObject { put("type", "resize"); put("cols", cols); put("rows", rows) }.toString())
    }

    fun close() {
        closed = true
        ws.close(1000, null)
    }
}

@Composable
fun ContainerTerminal(name: String, onBack: () -> Unit) {
    val ctx = LocalContext.current
    var mode by rememberSaveable { mutableStateOf("shell") }
    var attempt by remember { mutableIntStateOf(0) }
    var ended by remember { mutableStateOf<String?>(null) }
    var ctrl by remember { mutableStateOf(false) }
    var size by remember { mutableStateOf(80 to 24) }
    val screen = remember(mode, attempt) { TermScreen() }
    var view by remember { mutableStateOf<TerminalView?>(null) }
    var session by remember { mutableStateOf<TermSession?>(null) }
    DisposableEffect(mode, attempt) {
        ended = null
        val s = runCatching {
            TermSession(api(), name, mode, screen, onOutput = { view?.changed() }, onEnd = { e -> ended = e ?: "" })
        }.onFailure { ended = errorText(ctx, it) }.getOrNull()
        session = s
        s?.resize(size.first, size.second)
        onDispose { s?.close() }
    }
    val send: (String) -> Unit = { session?.input(it) }
    AreaScreen(name, onBack, subtitle = stringResource(if (mode == "shell") R.string.ct_shell else R.string.ct_console), actions = {
        IconButton(onClick = { view?.showKeyboard() }) { Icon(Icons.Default.Keyboard, stringResource(R.string.ct_keyboard)) }
        IconButton(onClick = { attempt++ }) { Icon(Icons.Default.Refresh, stringResource(R.string.ct_reconnect)) }
    }) { pad ->
        Column(Modifier.fillMaxSize().padding(pad).imePadding()) {
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                FilterChip(mode == "shell", { mode = "shell" }, { Text(stringResource(R.string.ct_shell)) })
                FilterChip(mode == "console", { mode = "console" }, { Text(stringResource(R.string.ct_console)) })
            }
            Text(stringResource(if (mode == "console") R.string.ct_console_help else R.string.ct_shell_help), Modifier.padding(horizontal = 12.dp),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            key(screen) {
                AndroidView(
                    factory = { c ->
                        TerminalView(c, screen, send = { s -> send(s) }, onResize = { cols, rows -> size = cols to rows; session?.resize(cols, rows) }).also { v ->
                            v.onCtrlUsed = { ctrl = false }
                            view = v
                            v.post { v.showKeyboard() }
                        }
                    },
                    update = { it.ctrl = ctrl },
                    modifier = Modifier.weight(1f).fillMaxWidth().background(Color(TerminalView.BG)),
                )
            }
            ended?.let { e ->
                Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
                    Text(if (e.isEmpty()) stringResource(R.string.ct_term_closed) else stringResource(R.string.ct_term_failed, e), Modifier.weight(1f).padding(top = 12.dp),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
                    TextButton(onClick = { attempt++ }) { Text(stringResource(R.string.ct_reconnect)) }
                }
            }
            // Keys a phone keyboard lacks.
            Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(4.dp), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                fun named(n: String) = { send(TermKeys.named(n, screen.appCursor)) }
                FilterChip(ctrl, { ctrl = !ctrl }, { Text("Ctrl") })
                listOf("Esc" to named("esc"), "Tab" to named("tab"), "←" to named("left"), "↑" to named("up"), "↓" to named("down"), "→" to named("right"),
                    "Home" to named("home"), "End" to named("end"), "PgUp" to named("pgup"), "PgDn" to named("pgdn"),
                    "|" to { view?.typed("|") ?: Unit }, "~" to { view?.typed("~") ?: Unit }, "/" to { view?.typed("/") ?: Unit }, "-" to { view?.typed("-") ?: Unit })
                    .forEach { (label, act) -> TextButton(onClick = { act() }) { Text(label) } }
            }
        }
    }
}

@Composable
private fun key(k: Any, content: @Composable () -> Unit) = androidx.compose.runtime.key(k) { content() }
