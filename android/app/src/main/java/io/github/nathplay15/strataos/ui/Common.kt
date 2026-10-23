package io.github.nathplay15.strataos.ui

import android.content.Context
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.CertificateChangedException
import io.github.nathplay15.strataos.net.UntrustedCertificateException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.text.DateFormat
import java.time.OffsetDateTime
import java.util.Date
import javax.net.ssl.SSLHandshakeException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

sealed interface Loaded<out T> {
    data object Loading : Loaded<Nothing>
    data class Ok<T>(val value: T) : Loaded<T>
    data class Failed(val error: Throwable) : Loaded<Nothing>
}

/** Loads a value off the main thread; [reload] runs it again. */
class Loader<T>(val state: Loaded<T>, val reload: () -> Unit)

@Composable
fun <T> rememberLoader(vararg keys: Any?, block: suspend () -> T): Loader<T> {
    var tick by remember { mutableIntStateOf(0) }
    var state by remember(*keys) { mutableStateOf<Loaded<T>>(Loaded.Loading) }
    LaunchedEffect(*keys, tick) {
        if (state !is Loaded.Ok) state = Loaded.Loading
        state = try {
            Loaded.Ok(withContext(Dispatchers.IO) { block() })
        } catch (e: Exception) {
            Loaded.Failed(e)
        }
    }
    return Loader(state) { tick++ }
}

/** A readable message for what went wrong. */
fun errorText(ctx: Context, e: Throwable): String {
    var c: Throwable? = e
    while (c != null) {
        when (c) {
            is CertificateChangedException -> return ctx.getString(R.string.err_cert_changed, c.presented)
            is UntrustedCertificateException -> return ctx.getString(R.string.err_cert_untrusted)
            is ApiException -> return if (c.status == 401) ctx.getString(R.string.err_unauthorized) else (c.message ?: "HTTP ${c.status}")
            is UnknownHostException -> return ctx.getString(R.string.err_unknown_host)
            is ConnectException, is SocketTimeoutException -> return ctx.getString(R.string.err_unreachable)
            is SSLHandshakeException -> if (c.cause == null) return ctx.getString(R.string.err_tls, c.message ?: "")
        }
        c = c.cause
    }
    return e.message ?: e.javaClass.simpleName
}

@Composable
fun <T> LoadedContent(loader: Loader<T>, content: @Composable (T) -> Unit) {
    when (val s = loader.state) {
        Loaded.Loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
        is Loaded.Failed -> ErrorPane(s.error, loader.reload)
        is Loaded.Ok -> content(s.value)
    }
}

@Composable
fun ErrorPane(e: Throwable, retry: () -> Unit) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    Column(Modifier.fillMaxSize().padding(24.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        Text(errorText(ctx, e), style = MaterialTheme.typography.bodyLarge, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth())
        Button(onClick = retry, modifier = Modifier.padding(top = 16.dp)) { Text(stringResource(R.string.retry)) }
    }
}

fun formatBytes(n: Long): String {
    if (n < 1024) return "$n B"
    val units = listOf("KB", "MB", "GB", "TB", "PB")
    var v = n.toDouble() / 1024
    var i = 0
    while (v >= 1024 && i < units.size - 1) { v /= 1024; i++ }
    return if (v >= 100) "%.0f %s".format(v, units[i]) else "%.1f %s".format(v, units[i])
}

fun formatDate(iso: String): String =
    runCatching { DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date.from(OffsetDateTime.parse(iso).toInstant())) }.getOrDefault("")

fun formatTime(ms: Long): String = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT).format(Date(ms))
