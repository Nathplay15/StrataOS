package io.github.nathplay15.strataos.ui.vms

import android.graphics.Bitmap
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.FitScreen
import androidx.compose.material.icons.filled.Keyboard
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.filled.PanTool
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.FilterQuality
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.vncUrl
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.errorText
import io.github.nathplay15.strataos.vnc.Keysym
import io.github.nathplay15.strataos.vnc.RfbClient
import io.github.nathplay15.strataos.vnc.RfbListener
import io.github.nathplay15.strataos.vnc.SENTINEL
import io.github.nathplay15.strataos.vnc.WsTransport
import io.github.nathplay15.strataos.vnc.charKeysym
import io.github.nathplay15.strataos.vnc.fitScale
import io.github.nathplay15.strataos.vnc.imeDiff
import io.github.nathplay15.strataos.vnc.keyCodeKeysym
import io.github.nathplay15.strataos.vnc.viewToScreen
import kotlin.concurrent.thread

/**
 * The screen of a VM (VNC through the NAS). A tap clicks, a long press
 * right-clicks, two fingers zoom and move; in drag mode one finger holds the
 * left button. The keyboard button opens the on-screen keyboard; special keys
 * and Ctrl+Alt+Del are in the bar.
 */
@Composable
fun VncScreen(vm: String, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val res = LocalResources.current
    var status by remember { mutableStateOf<String?>(null) }
    var connected by remember { mutableStateOf(false) }
    var attempt by remember { mutableIntStateOf(0) }
    var size by remember { mutableStateOf(IntSize.Zero) }
    var dirty by remember { mutableIntStateOf(0) }
    var client by remember { mutableStateOf<RfbClient?>(null) }
    var bitmap by remember { mutableStateOf<Bitmap?>(null) }
    var frame by remember { mutableIntStateOf(0) }

    DisposableEffect(vm, attempt) {
        status = null
        connected = false
        val api = api()
        val t = runCatching { WsTransport(api.http, api.request(api.vncUrl(vm)).build()) }
        var c: RfbClient? = null
        t.onSuccess { tr ->
            c = RfbClient(tr, object : RfbListener {
                override fun onResize(width: Int, height: Int, name: String) { size = IntSize(width, height); connected = true }
                override fun onUpdate(x: Int, y: Int, w: Int, h: Int) { dirty++ }
            })
            client = c
            thread(name = "vnc-$vm") {
                try {
                    c!!.run()
                    status = res.getString(R.string.vm_console_closed)
                } catch (e: Exception) {
                    status = errorText(ctx, e)
                }
                connected = false
            }
        }.onFailure { status = errorText(ctx, it) }
        onDispose { c?.close(); client = null }
    }
    // Copies the framebuffer into the bitmap at most once per frame.
    LaunchedEffect(size) {
        if (size.width <= 0) return@LaunchedEffect
        val bm = Bitmap.createBitmap(size.width, size.height, Bitmap.Config.ARGB_8888)
        bitmap = bm
        var seen = -1
        while (true) {
            withFrameNanos { }
            val d = dirty
            val c = client ?: continue
            if (d != seen && c.pixels.size == bm.width * bm.height) {
                seen = d
                bm.setPixels(c.pixels, 0, bm.width, 0, 0, bm.width, bm.height)
                frame++
            }
        }
    }

    var scale by remember { mutableFloatStateOf(1f) }
    var off by remember { mutableStateOf(Offset.Zero) }
    var view by remember { mutableStateOf(IntSize.Zero) }
    var last by remember { mutableStateOf(0 to 0) }
    var drag by remember { mutableStateOf(false) }
    var ctrl by remember { mutableStateOf(false) }
    var alt by remember { mutableStateOf(false) }
    var keysMenu by remember { mutableStateOf(false) }
    val focus = remember { FocusRequester() }
    val kb = LocalSoftwareKeyboardController.current
    var ime by remember { mutableStateOf(TextFieldValue(SENTINEL, TextRange(SENTINEL.length))) }

    fun fit() {
        if (size.width <= 0 || view.width <= 0) return
        scale = fitScale(size.width, size.height, view.width.toFloat(), view.height.toFloat())
        off = Offset((view.width - size.width * scale) / 2, (view.height - size.height * scale) / 2)
    }
    LaunchedEffect(size, view) { fit() }

    fun send(f: (RfbClient) -> Unit) { client?.let { c -> thread { runCatching { f(c) } } } }
    /** Sends keys, with the sticky modifiers (then released) around them. */
    fun press(keys: List<Int>) {
        val mods = listOfNotNull(Keysym.CONTROL_L.takeIf { ctrl }, Keysym.ALT_L.takeIf { alt })
        ctrl = false; alt = false
        send { c ->
            mods.forEach { c.key(it, true) }
            keys.forEach { c.key(it, true); c.key(it, false) }
            mods.asReversed().forEach { c.key(it, false) }
        }
    }
    fun at(p: Offset) = viewToScreen(p.x, p.y, scale, off.x, off.y, size.width, size.height)

    AreaScreen(vm, onBack, subtitle = stringResource(R.string.vm_console)) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().imePadding()) {
            Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = { focus.requestFocus(); kb?.show() }) { Icon(Icons.Default.Keyboard, stringResource(R.string.vm_keyboard)) }
                TextButton(onClick = {
                    send { c -> listOf(Keysym.CONTROL_L, Keysym.ALT_L, Keysym.DELETE).forEach { c.key(it, true) }; listOf(Keysym.DELETE, Keysym.ALT_L, Keysym.CONTROL_L).forEach { c.key(it, false) } }
                }) { Text("Ctrl+Alt+Del") }
                FilterChip(selected = ctrl, onClick = { ctrl = !ctrl }, label = { Text("Ctrl") })
                FilterChip(selected = alt, onClick = { alt = !alt }, label = { Text("Alt") }, modifier = Modifier.padding(start = 4.dp))
                Box {
                    TextButton(onClick = { keysMenu = true }) { Text(stringResource(R.string.vm_keys)) }
                    DropdownMenu(keysMenu, { keysMenu = false }) {
                        val keys = listOf("Esc" to Keysym.ESCAPE, "Tab" to Keysym.TAB, "Enter" to Keysym.RETURN, "Super" to Keysym.SUPER_L,
                            "←" to Keysym.LEFT, "↑" to Keysym.UP, "→" to Keysym.RIGHT, "↓" to Keysym.DOWN, "Home" to Keysym.HOME, "End" to Keysym.END,
                            "Page Up" to Keysym.PAGE_UP, "Page Down" to Keysym.PAGE_DOWN, "Insert" to Keysym.INSERT, "Delete" to Keysym.DELETE) +
                            (1..12).map { "F$it" to Keysym.f(it) }
                        keys.forEach { (l, k) -> DropdownMenuItem(text = { Text(l) }, onClick = { keysMenu = false; press(listOf(k)) }) }
                    }
                }
                IconButton(onClick = { drag = !drag }) {
                    Icon(Icons.Default.PanTool, stringResource(R.string.vm_drag_mode), tint = if (drag) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                }
                IconButton(onClick = { send { c -> val (x, y) = at(Offset(view.width / 2f, view.height / 2f)); c.pointer(x, y, 8); c.pointer(x, y, 0) } }) {
                    Icon(Icons.Default.KeyboardArrowUp, stringResource(R.string.vm_scroll_up))
                }
                IconButton(onClick = { send { c -> val (x, y) = at(Offset(view.width / 2f, view.height / 2f)); c.pointer(x, y, 16); c.pointer(x, y, 0) } }) {
                    Icon(Icons.Default.KeyboardArrowDown, stringResource(R.string.vm_scroll_down))
                }
                IconButton(onClick = { fit() }) { Icon(Icons.Default.FitScreen, stringResource(R.string.vm_fit)) }
            }
            // Receives the on-screen keyboard (and hardware keys) for the VM.
            BasicTextField(
                value = ime,
                onValueChange = { v ->
                    val keys = imeDiff(v.text)
                    if (keys.isNotEmpty()) press(keys)
                    ime = TextFieldValue(SENTINEL, TextRange(SENTINEL.length))
                },
                modifier = Modifier.size(1.dp).focusRequester(focus).onPreviewKeyEvent { e ->
                    val k = keyCodeKeysym(e.nativeKeyEvent.keyCode)
                    if (k == 0) return@onPreviewKeyEvent false
                    val down = e.type == KeyEventType.KeyDown
                    send { it.key(k, down) }
                    true
                },
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false, imeAction = ImeAction.None),
                keyboardActions = KeyboardActions(onAny = { press(listOf(charKeysym('\n'.code))) }),
            )
            Box(Modifier.fillMaxSize().background(Color.Black).onSizeChanged { view = it }) {
                val bm = bitmap
                val desc = stringResource(R.string.vm_screen_desc, vm)
                if (bm != null) Canvas(Modifier.fillMaxSize().semantics { contentDescription = desc }
                    .pointerInput(size, drag) {
                        detectTransformGestures { centroid, pan, zoom, _ ->
                            if (drag && zoom == 1f) {
                                val p = at(centroid)
                                last = p
                                send { it.pointer(p.first, p.second, 1) }
                            } else {
                                val ns = (scale * zoom).coerceIn(0.2f, 8f)
                                off = centroid - (centroid - off) * (ns / scale) + pan
                                scale = ns
                            }
                        }
                    }
                    .pointerInput(size, drag) {
                        detectTapGestures(
                            onTap = { p -> val (x, y) = at(p); send { it.pointer(x, y, 0); it.pointer(x, y, 1); it.pointer(x, y, 0) } },
                            onDoubleTap = { p -> val (x, y) = at(p); send { repeat(2) { _ -> it.pointer(x, y, 1); it.pointer(x, y, 0) } } },
                            onLongPress = { p -> val (x, y) = at(p); send { it.pointer(x, y, 4); it.pointer(x, y, 0) } },
                            onPress = { if (drag) { tryAwaitRelease(); val (x, y) = last; send { it.pointer(x, y, 0) } } },
                        )
                    }) {
                    @Suppress("UNUSED_EXPRESSION") frame
                    withTransform({ translate(off.x, off.y); scale(scale, scale, Offset.Zero) }) {
                        drawImage(bm.asImageBitmap(), filterQuality = if (scale < 1f) FilterQuality.Medium else FilterQuality.None)
                    }
                }
                when {
                    status != null -> Surface(Modifier.align(Alignment.Center).padding(24.dp), shape = MaterialTheme.shapes.medium) {
                        Column(Modifier.padding(16.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(12.dp)) {
                            Text(status ?: "")
                            Button(onClick = { attempt++ }) { Text(stringResource(R.string.vm_reconnect)) }
                        }
                    }
                    !connected -> CircularProgressIndicator(Modifier.align(Alignment.Center))
                }
            }
        }
    }
}
