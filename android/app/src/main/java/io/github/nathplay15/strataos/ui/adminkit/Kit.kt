package io.github.nathplay15.strataos.ui.adminkit

import android.content.Context
import android.widget.Toast
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExposedDropdownMenuAnchorType
import androidx.compose.material3.ExposedDropdownMenuBox
import androidx.compose.material3.ExposedDropdownMenuDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.job
import io.github.nathplay15.strataos.ui.ErrorPane
import io.github.nathplay15.strataos.ui.Loaded
import io.github.nathplay15.strataos.ui.errorText
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// Building blocks of the management areas (surveillance, VMs, VPN, users,
// Multi-NAS, settings): screens with their own top bar and back arrow, pull to
// refresh, loading/error/empty states, confirmations and long-running jobs.

/** The API of the paired NAS (the management areas only show while signed in). */
fun api(): Api = Graph.api() ?: throw IllegalStateException("signed out")

/** A value loaded from the NAS: [refreshing] while a pull to refresh runs over a shown value. */
class Data<T>(val state: Loaded<T>, val refreshing: Boolean, val reload: () -> Unit) {
    val value: T? get() = (state as? Loaded.Ok<T>)?.value
}

/**
 * Loads [block] off the main thread, again on [Data.reload] and, with
 * [every] (ms), periodically while shown (silently: the value stays shown).
 */
@Composable
fun <T> rememberData(vararg keys: Any?, every: Long = 0, block: suspend (Api) -> T): Data<T> {
    var tick by remember { mutableIntStateOf(0) }
    var state by remember(*keys) { mutableStateOf<Loaded<T>>(Loaded.Loading) }
    var refreshing by remember { mutableStateOf(false) }
    LaunchedEffect(*keys, tick) {
        while (true) {
            state = try {
                Loaded.Ok(withContext(Dispatchers.IO) { block(api()) })
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                // A periodic refresh that fails keeps the last value.
                if (state is Loaded.Ok && !refreshing && every > 0) state else Loaded.Failed(e)
            }
            refreshing = false
            if (every <= 0) break
            delay(every)
        }
    }
    return Data(state, refreshing) { if (state is Loaded.Ok) refreshing = true; tick++ }
}

val LocalSnackbar = staticCompositionLocalOf<SnackbarHostState?> { null }

/**
 * A screen of a management area: top bar with a back arrow (system back does
 * the same), optional actions and floating button, and a snackbar for messages.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AreaScreen(
    title: String,
    onBack: () -> Unit,
    subtitle: String? = null,
    actions: @Composable RowScope.() -> Unit = {},
    fab: @Composable () -> Unit = {},
    systemBack: (() -> Unit)? = null,
    content: @Composable (PaddingValues) -> Unit,
) {
    BackHandler(onBack = systemBack ?: onBack)
    val snack = remember { SnackbarHostState() }
    CompositionLocalProvider(LocalSnackbar provides snack) {
        Scaffold(
            contentWindowInsets = WindowInsets(0, 0, 0, 0),
            topBar = {
                TopAppBar(
                    title = {
                        Column {
                            Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            if (!subtitle.isNullOrEmpty()) Text(subtitle, style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                    },
                    navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.b_back)) } },
                    actions = actions,
                )
            },
            floatingActionButton = fab,
            snackbarHost = { SnackbarHost(snack) },
        ) { pad -> content(pad) }
    }
}

/** Pull to refresh around a loaded value, with the loading and error states. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun <T> Refreshable(data: Data<T>, pad: PaddingValues, content: @Composable (T) -> Unit) {
    PullToRefreshBox(isRefreshing = data.refreshing, onRefresh = data.reload, modifier = Modifier.fillMaxSize().padding(pad)) {
        when (val s = data.state) {
            Loaded.Loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
            is Loaded.Failed -> Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) { ErrorPane(s.error, data.reload) }
            is Loaded.Ok -> content(s.value)
        }
    }
}

/** A scrolling column of cards, centred and bounded on tablets. */
@Composable
fun ScrollColumn(content: @Composable ColumnScope.() -> Unit) {
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp).wrapContentWidth(Alignment.CenterHorizontally).widthIn(max = 840.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
        content = content,
    )
}

/** Width of list content on tablets. */
fun Modifier.readable() = fillMaxWidth().wrapContentWidth(Alignment.CenterHorizontally).widthIn(max = 840.dp)

@Composable
fun Empty(icon: ImageVector, text: String, action: (@Composable () -> Unit)? = null) {
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(32.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        Icon(icon, null, Modifier.size(56.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(text, Modifier.padding(top = 12.dp), style = MaterialTheme.typography.bodyLarge, textAlign = TextAlign.Center, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (action != null) Box(Modifier.padding(top = 16.dp)) { action() }
    }
}

@Composable
fun SectionCard(title: String? = null, modifier: Modifier = Modifier, content: @Composable ColumnScope.() -> Unit) {
    Card(modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            if (title != null) Text(title, style = MaterialTheme.typography.titleMedium)
            content()
        }
    }
}

@Composable
fun KeyValue(label: String, value: String, mono: Boolean = false) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.Top) {
        Text(label, Modifier.weight(0.4f), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(value, Modifier.weight(0.6f), style = MaterialTheme.typography.bodyMedium, fontFamily = if (mono) FontFamily.Monospace else null)
    }
}

@Composable
fun SwitchLine(text: String, checked: Boolean, enabled: Boolean = true, sub: String? = null, onChange: (Boolean) -> Unit) {
    Row(
        Modifier.fillMaxWidth().semantics(mergeDescendants = true) { role = Role.Switch }.padding(vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(text, style = MaterialTheme.typography.bodyLarge)
            if (!sub.isNullOrEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Switch(checked = checked, onCheckedChange = onChange, enabled = enabled)
    }
}

/** A small coloured label (state of a VM, a camera, a user…). */
@Composable
fun StatusBadge(text: String, tone: Tone) {
    val (bg, fg) = when (tone) {
        Tone.Ok -> Color(0xFF1E9E5A).copy(alpha = 0.18f) to Color(0xFF1E9E5A)
        Tone.Warn -> Color(0xFFE0A100).copy(alpha = 0.18f) to Color(0xFFB98500)
        Tone.Bad -> MaterialTheme.colorScheme.errorContainer to MaterialTheme.colorScheme.onErrorContainer
        Tone.Neutral -> MaterialTheme.colorScheme.surfaceVariant to MaterialTheme.colorScheme.onSurfaceVariant
        Tone.Accent -> MaterialTheme.colorScheme.primaryContainer to MaterialTheme.colorScheme.onPrimaryContainer
    }
    Surface(color = bg, shape = MaterialTheme.shapes.small) {
        Text(text, Modifier.padding(horizontal = 8.dp, vertical = 2.dp), style = MaterialTheme.typography.labelMedium, color = fg, maxLines = 1)
    }
}

enum class Tone { Ok, Warn, Bad, Neutral, Accent }

/** A text field; [number] shows the numeric keyboard, [secret] hides the text. */
@Composable
fun Field(
    label: String, value: String, onChange: (String) -> Unit, modifier: Modifier = Modifier, number: Boolean = false,
    secret: Boolean = false, error: String? = null, help: String? = null, enabled: Boolean = true, singleLine: Boolean = true,
) {
    OutlinedTextField(
        value = value, onValueChange = onChange, label = { Text(label) }, modifier = modifier.fillMaxWidth(), enabled = enabled,
        singleLine = singleLine, isError = error != null,
        supportingText = (error ?: help)?.let { { Text(it) } },
        visualTransformation = if (secret) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
        keyboardOptions = KeyboardOptions(keyboardType = when {
            number -> KeyboardType.Number
            secret -> KeyboardType.Password
            else -> KeyboardType.Text
        }),
    )
}

/** A choice among [options] (value to label). */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun <T> Choice(label: String, options: List<Pair<T, String>>, selected: T, onSelect: (T) -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    var open by remember { mutableStateOf(false) }
    ExposedDropdownMenuBox(expanded = open, onExpandedChange = { if (enabled) open = it }, modifier = modifier.fillMaxWidth()) {
        OutlinedTextField(
            value = options.firstOrNull { it.first == selected }?.second ?: selected.toString(), onValueChange = {}, readOnly = true,
            label = { Text(label) }, enabled = enabled, singleLine = true,
            trailingIcon = { ExposedDropdownMenuDefaults.TrailingIcon(expanded = open) },
            modifier = Modifier.fillMaxWidth().menuAnchor(ExposedDropdownMenuAnchorType.PrimaryNotEditable, enabled),
        )
        ExposedDropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            options.forEach { (v, l) -> DropdownMenuItem(text = { Text(l) }, onClick = { onSelect(v); open = false }) }
        }
    }
}

/**
 * Confirmation of an action. [typeToConfirm]: the name the user must type
 * first (deleting a VM, a user, restarting the NAS…).
 */
@Composable
fun Confirm(
    title: String, text: String, confirm: String, onConfirm: () -> Unit, onDismiss: () -> Unit,
    destructive: Boolean = true, typeToConfirm: String? = null, extra: (@Composable ColumnScope.() -> Unit)? = null,
) {
    var typed by remember { mutableStateOf("") }
    val ok = typeToConfirm == null || typed.trim() == typeToConfirm
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text(text)
                extra?.invoke(this)
                if (typeToConfirm != null) {
                    Text(stringResource(R.string.b_type_to_confirm, typeToConfirm), style = MaterialTheme.typography.bodySmall)
                    OutlinedTextField(typed, { typed = it }, singleLine = true, modifier = Modifier.fillMaxWidth(), label = { Text(typeToConfirm) })
                }
            }
        },
        confirmButton = {
            TextButton(onClick = { onDismiss(); onConfirm() }, enabled = ok) {
                Text(confirm, color = if (destructive && ok) MaterialTheme.colorScheme.error else androidx.compose.ui.graphics.Color.Unspecified)
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } },
    )
}

/** Runs NAS calls from buttons: [busy] meanwhile, errors and results in the snackbar. */
class Actions(private val scope: CoroutineScope, private val ctx: Context, private val snack: SnackbarHostState?) {
    var busy by mutableStateOf(false)
        private set

    fun run(done: String? = null, after: () -> Unit = {}, block: suspend (Api) -> Unit) {
        if (busy) return
        busy = true
        scope.launch {
            try {
                withContext(Dispatchers.IO) { block(api()) }
                done?.let { say(it) }
                after()
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                say(errorText(ctx, e))
            } finally {
                busy = false
            }
        }
    }

    fun say(msg: String) {
        if (snack != null) scope.launch { snack.showSnackbar(msg) } else Toast.makeText(ctx, msg, Toast.LENGTH_LONG).show()
    }
}

@Composable
fun rememberActions(): Actions {
    val scope = rememberCoroutineScope()
    val ctx = LocalContext.current
    val snack = LocalSnackbar.current
    return remember(snack) { Actions(scope, ctx, snack) }
}

/**
 * Follows a job of the NAS (VM creation, updates, installs) until it ends:
 * progress and the last lines of its log. Closing only hides it.
 */
@Composable
fun JobDialog(start: NasJob, onClose: (success: Boolean?) -> Unit) {
    var j by remember { mutableStateOf(start) }
    val ctx = LocalContext.current
    var err by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(start.id) {
        while (j.running && start.id.isNotEmpty()) {
            delay(1500)
            try {
                j = withContext(Dispatchers.IO) { api().job(start.id) }
                err = null
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                err = errorText(ctx, e) // the NAS restarting (StrataOS update): keep trying
            }
        }
    }
    AlertDialog(
        onDismissRequest = {},
        title = { Text(j.title.ifEmpty { start.title }) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                when {
                    j.running -> {
                        if (j.progress > 0) LinearProgressIndicator(progress = { (j.progress / 100).toFloat() }, Modifier.fillMaxWidth())
                        else LinearProgressIndicator(Modifier.fillMaxWidth())
                        Text(stringResource(R.string.b_job_running), style = MaterialTheme.typography.bodySmall)
                    }
                    j.status == "success" -> Text(stringResource(R.string.b_job_done), color = Color(0xFF1E9E5A))
                    else -> Text(stringResource(R.string.b_job_failed, j.error), color = MaterialTheme.colorScheme.error)
                }
                err?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                val tail = j.log.trimEnd().lines().takeLast(10).joinToString("\n")
                if (tail.isNotBlank()) Text(tail, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall, modifier = Modifier.verticalScroll(rememberScrollState()))
            }
        },
        confirmButton = {
            TextButton(onClick = { onClose(if (j.running) null else j.status == "success") }) {
                Text(stringResource(if (j.running) R.string.b_job_hide else R.string.b_ok))
            }
        },
    )
}
