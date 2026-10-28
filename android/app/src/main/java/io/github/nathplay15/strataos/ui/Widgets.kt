package io.github.nathplay15.strataos.ui

import android.content.Context
import android.widget.Toast
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.PrimaryScrollableTabRow
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.Job
import io.github.nathplay15.strataos.net.awaitJob
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// Shared building blocks of the v2 screens: loading with pull to refresh,
// empty and error states, confirmations, NAS jobs followed in a dialog.

/** A value loaded off the main thread; [refreshing] while a reload of a shown value runs. */
class Load<T>(val state: Loaded<T>, val refreshing: Boolean, val reload: () -> Unit) {
    val value: T? get() = (state as? Loaded.Ok)?.value
}

@Composable
fun <T> rememberLoad(vararg keys: Any?, block: suspend () -> T): Load<T> {
    var tick by remember { mutableIntStateOf(0) }
    var state by remember(*keys) { mutableStateOf<Loaded<T>>(Loaded.Loading) }
    var refreshing by remember { mutableStateOf(false) }
    LaunchedEffect(*keys, tick) {
        refreshing = state is Loaded.Ok
        if (!refreshing) state = Loaded.Loading
        state = try {
            Loaded.Ok(withContext(Dispatchers.IO) { block() })
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            Loaded.Failed(e)
        }
        refreshing = false
    }
    return Load(state, refreshing) { tick++ }
}

/** The API of the paired NAS (screens are only shown while signed in). */
fun api(): Api = Graph.api() ?: throw IllegalStateException("signed out")

/**
 * Loading spinner, error with retry, or [content], with pull to refresh.
 * [content] should scroll (LazyColumn, verticalScroll) for the gesture to work.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun <T> Refreshable(load: Load<T>, modifier: Modifier = Modifier, content: @Composable (T) -> Unit) {
    PullToRefreshBox(isRefreshing = load.refreshing, onRefresh = load.reload, modifier = modifier.fillMaxSize()) {
        when (val s = load.state) {
            Loaded.Loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
            is Loaded.Failed -> Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) { ErrorPane(s.error, load.reload) }
            is Loaded.Ok -> content(s.value)
        }
    }
}

@Composable
fun EmptyState(icon: ImageVector, text: String, modifier: Modifier = Modifier, action: (@Composable () -> Unit)? = null) {
    Column(modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(32.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        Icon(icon, null, Modifier.size(56.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.6f))
        Text(text, Modifier.padding(top = 12.dp), color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        action?.let { Box(Modifier.padding(top = 16.dp)) { it() } }
    }
}

/** A screen of a management area: top bar with back, optional tabs. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SectionScaffold(
    title: String,
    back: (() -> Unit)?,
    actions: @Composable RowScope.() -> Unit = {},
    tabs: List<String> = emptyList(),
    tab: Int = 0,
    onTab: (Int) -> Unit = {},
    fab: @Composable () -> Unit = {},
    content: @Composable (Modifier) -> Unit,
) {
    Scaffold(
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
        topBar = {
            Column {
                TopAppBar(
                    title = { Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    navigationIcon = { if (back != null) IconButton(onClick = back) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
                    actions = actions,
                )
                if (tabs.isNotEmpty()) PrimaryScrollableTabRow(selectedTabIndex = tab, edgePadding = 8.dp) {
                    tabs.forEachIndexed { i, t -> Tab(selected = tab == i, onClick = { onTab(i) }, text = { Text(t) }) }
                }
            }
        },
        floatingActionButton = fab,
    ) { pad -> content(Modifier.padding(pad).fillMaxSize()) }
}

@Composable
fun RefreshAction(reload: () -> Unit) {
    IconButton(onClick = reload) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) }
}

/** A small colored label (health, state). */
@Composable
fun Chip(text: String, tone: Tone = Tone.NEUTRAL) {
    val c = tone.color()
    Text(
        text, color = c, fontSize = 12.sp, maxLines = 1,
        modifier = Modifier.background(c.copy(alpha = 0.14f), RoundedCornerShape(50)).padding(horizontal = 8.dp, vertical = 2.dp),
    )
}

enum class Tone { OK, WARN, DANGER, ACCENT, NEUTRAL }

@Composable
fun Tone.color(): Color = when (this) {
    Tone.OK -> Color(0xFF1E9E5A)
    Tone.WARN -> Color(0xFFE0A100)
    Tone.DANGER -> MaterialTheme.colorScheme.error
    Tone.ACCENT -> MaterialTheme.colorScheme.primary
    Tone.NEUTRAL -> MaterialTheme.colorScheme.onSurfaceVariant
}

/** Tone of a health or state word of the NAS (healthy, degraded, running…). */
fun toneOf(state: String): Tone = when (state.lowercase()) {
    "healthy", "online", "ok", "active", "running", "clean", "success", "passed", "current", "up" -> Tone.OK
    "degraded", "rebuilding", "resyncing", "paused", "restarting", "starting", "warning", "available", "partial", "unmounted" -> Tone.WARN
    "failed", "faulted", "unavail", "removed", "error", "dead", "exited", "inactive", "unhealthy", "offline" -> Tone.DANGER
    else -> Tone.NEUTRAL
}

@Composable
fun UsageBar(used: Long, total: Long, modifier: Modifier = Modifier) {
    val frac = if (total > 0) (used.toFloat() / total).coerceIn(0f, 1f) else 0f
    Column(modifier) {
        LinearProgressIndicator(progress = { frac }, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
            color = if (frac > 0.9f) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary)
        Text(stringResource(R.string.a_usage, formatBytes(used), formatBytes(total), (frac * 100).toInt()), style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/**
 * Confirmation of an action. [danger] colors it; [typeToConfirm] asks to type
 * a name first (what erases disks or data); [details] lists what is affected.
 */
@Composable
fun ConfirmDialog(
    title: String,
    text: String,
    confirm: String,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
    danger: Boolean = false,
    typeToConfirm: String? = null,
    details: List<String> = emptyList(),
    extra: @Composable ColumnScope.() -> Unit = {},
) {
    var typed by remember { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(text)
                details.forEach { Text("• $it", style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace) }
                extra()
                if (typeToConfirm != null) {
                    Text(stringResource(R.string.a_type_to_confirm, typeToConfirm), style = MaterialTheme.typography.bodySmall)
                    OutlinedTextField(typed, { typed = it }, singleLine = true, modifier = Modifier.fillMaxWidth())
                }
            }
        },
        confirmButton = {
            TextButton(enabled = typeToConfirm == null || typed.trim() == typeToConfirm, onClick = { onDismiss(); onConfirm() }) {
                Text(confirm, color = if (danger) MaterialTheme.colorScheme.error else Color.Unspecified)
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } },
    )
}

/** Asks for one line of text (names). */
@Composable
fun TextDialog(
    title: String,
    label: String,
    initial: String = "",
    confirm: String,
    valid: (String) -> Boolean = { it.isNotBlank() },
    number: Boolean = false,
    onDismiss: () -> Unit,
    onConfirm: (String) -> Unit,
) {
    var v by remember { mutableStateOf(initial) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = {
            OutlinedTextField(v, { v = it }, singleLine = true, label = { Text(label) }, modifier = Modifier.fillMaxWidth(),
                keyboardOptions = if (number) KeyboardOptions(keyboardType = KeyboardType.Number) else KeyboardOptions.Default)
        },
        confirmButton = { TextButton(enabled = valid(v.trim()), onClick = { onDismiss(); onConfirm(v.trim()) }) { Text(confirm) } },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } },
    )
}

fun toast(ctx: Context, msg: String) = Toast.makeText(ctx, msg, Toast.LENGTH_LONG).show()

/** Runs NAS calls from a screen: errors become toasts, jobs are followed in a dialog. */
class Actions(private val ctx: Context, private val scope: CoroutineScope, private val job: MutableState<Job?>, val busy: MutableState<Boolean>) {
    /** Runs [block] off the main thread, then [done] on it (only on success). */
    fun run(done: () -> Unit = {}, block: suspend () -> Unit) {
        scope.launch {
            busy.value = true
            try {
                withContext(Dispatchers.IO) { block() }
                done()
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                toast(ctx, errorText(ctx, e))
            } finally {
                busy.value = false
            }
        }
    }

    /** Starts a NAS job with [start] and shows it until it ends; [done] runs afterwards. */
    fun job(done: () -> Unit = {}, start: suspend () -> Job) {
        scope.launch {
            busy.value = true
            try {
                val j = withContext(Dispatchers.IO) { start() }
                job.value = j
                api().awaitJob(j) { job.value = it }
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                job.value = null
                toast(ctx, errorText(ctx, e))
            } finally {
                busy.value = false
                done()
            }
        }
    }
}

@Composable
fun rememberActions(): Actions {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val job = remember { mutableStateOf<Job?>(null) }
    val busy = remember { mutableStateOf(false) }
    val a = remember { Actions(ctx, scope, job, busy) }
    job.value?.let { JobDialog(it) { job.value = null } }
    return a
}

/** Progress and log of a NAS job; closable at any time (the job goes on). */
@Composable
fun JobDialog(j: Job, onClose: () -> Unit) {
    AlertDialog(
        onDismissRequest = { if (!j.running) onClose() },
        title = { Text(j.title.ifEmpty { stringResource(R.string.a_job) }) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                when {
                    j.running -> {
                        if (j.progress > 0) LinearProgressIndicator(progress = { (j.progress / 100).toFloat() }, modifier = Modifier.fillMaxWidth())
                        else LinearProgressIndicator(Modifier.fillMaxWidth())
                        Text(stringResource(R.string.a_job_running), style = MaterialTheme.typography.bodySmall)
                    }
                    j.failed -> Text(j.error.ifEmpty { stringResource(R.string.a_job_failed) }, color = MaterialTheme.colorScheme.error)
                    else -> Text(stringResource(R.string.a_job_done), color = Tone.OK.color())
                }
                if (j.log.isNotBlank()) Text(
                    j.log.lines().takeLast(14).joinToString("\n"), fontFamily = FontFamily.Monospace, fontSize = 11.sp,
                    modifier = Modifier.fillMaxWidth().heightIn(max = 260.dp).verticalScroll(rememberScrollState())
                        .background(MaterialTheme.colorScheme.surfaceVariant, RoundedCornerShape(8.dp)).padding(8.dp),
                )
            }
        },
        confirmButton = { TextButton(onClick = onClose) { Text(stringResource(if (j.running) R.string.a_hide else R.string.a_close)) } },
    )
}

/** A label and a value on one line (properties, details). */
@Composable
fun InfoRow(label: String, value: String) {
    if (value.isEmpty()) return
    Row(Modifier.fillMaxWidth().padding(vertical = 3.dp)) {
        Text(label, Modifier.weight(0.4f), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Text(value, Modifier.weight(0.6f), style = MaterialTheme.typography.bodyMedium)
    }
}

/** Handles system back for a nested screen when [enabled]. */
@Composable
fun BackTo(enabled: Boolean = true, onBack: () -> Unit) = BackHandler(enabled, onBack)

/** Width at which screens show two panes (tablets, unfolded foldables). */
const val WIDE_DP = 600
