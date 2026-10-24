package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.QrCodeScanner
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import io.github.nathplay15.strataos.BuildConfig
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.backup.BackupScheduler
import io.github.nathplay15.strataos.data.PairLink
import io.github.nathplay15.strataos.data.ServerConfig
import io.github.nathplay15.strataos.data.hostPort
import io.github.nathplay15.strataos.data.normalizeAddress
import io.github.nathplay15.strataos.data.parsePairLink
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.CertInfo
import io.github.nathplay15.strataos.net.CertProbe
import io.github.nathplay15.strataos.net.Discovery
import io.github.nathplay15.strataos.net.FoundNas
import io.github.nathplay15.strataos.net.PairResponse
import io.github.nathplay15.strataos.notify.EventsWorker
import io.github.nathplay15.strataos.notify.Push
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** Where the NAS is and which certificate it was trusted with. */
private data class Target(val url: String, val fingerprint: String, val caOnly: Boolean)

/** First run: find or type the NAS, check its certificate, sign in (or scan the QR code). */
@Composable
fun Onboarding(initialLink: PairLink?, onLinkUsed: () -> Unit, onDone: () -> Unit) {
    val ctx = LocalContext.current
    val appRes = androidx.compose.ui.platform.LocalResources.current
    val scope = rememberCoroutineScope()
    var address by remember { mutableStateOf("") }
    var found by remember { mutableStateOf<List<FoundNas>>(emptyList()) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var cert by remember { mutableStateOf<Pair<String, CertInfo>?>(null) }
    var target by remember { mutableStateOf<Target?>(null) }

    DisposableEffect(Unit) {
        val d = Discovery(ctx) { found = it }
        d.start()
        onDispose { d.stop() }
    }

    fun check(url: String) {
        error = null
        busy = true
        scope.launch {
            try {
                if (url.startsWith("http://")) {
                    target = Target(url, "", false)
                } else {
                    val (h, p) = hostPort(url)
                    val info = withContext(Dispatchers.IO) { CertProbe.probe(h, p) }
                    if (info.publiclyTrusted) target = Target(url, info.fingerprint, false) else cert = url to info
                }
            } catch (e: Exception) {
                error = errorText(ctx, e)
            } finally {
                busy = false
            }
        }
    }

    fun finish(t: Target, r: PairResponse) {
        val p = Graph.prefs
        p.vault.save(r.token)
        p.server = ServerConfig(t.url, t.fingerprint, t.caOnly, r.user.username, r.user.fullName, r.user.role, r.server.hostname, r.device.id, r.device.name)
        Graph.reset()
        EventsWorker.schedule(ctx)
        BackupScheduler.apply(ctx)
        scope.launch(Dispatchers.IO) {
            runCatching { Graph.api()?.info() }.getOrNull()?.let { info -> withContext(Dispatchers.Main) { Push.register(ctx as android.app.Activity, info.vapidKey) } }
        }
        onDone()
    }

    // QR code: the address, the fingerprint and a one-time code; no password typed.
    fun pairWith(l: PairLink) {
        error = null
        busy = true
        scope.launch {
            try {
                var fp = l.fingerprint
                if (l.url.startsWith("https://")) {
                    val (h, p) = hostPort(l.url)
                    val info = withContext(Dispatchers.IO) { CertProbe.probe(h, p) }
                    if (fp.isNotEmpty() && info.fingerprint != fp) {
                        error = appRes.getString(R.string.err_qr_fingerprint, info.fingerprint)
                        return@launch
                    }
                    if (fp.isEmpty()) {
                        if (!info.publiclyTrusted) { cert = l.url to info; return@launch }
                        fp = info.fingerprint
                    }
                }
                val t = Target(l.url, fp, false)
                val r = withContext(Dispatchers.IO) {
                    Api(t.url, Graph.client(ServerConfig(t.url, t.fingerprint)), null).pairWithCode(l.code, Graph.deviceModel, Graph.deviceModel, BuildConfig.VERSION_NAME)
                }
                finish(t, r)
            } catch (e: Exception) {
                error = errorText(ctx, e)
            } finally {
                busy = false
            }
        }
    }

    // A pairing link opened from outside the app (QR code scanned with the camera app).
    LaunchedEffect(initialLink) { initialLink?.let { pairWith(it); onLinkUsed() } }

    val scan = androidx.activity.compose.rememberLauncherForActivityResult(ScanContract()) { res ->
        val l = res.contents?.let { parsePairLink(it) }
        if (res.contents != null && l == null) error = appRes.getString(R.string.err_qr_invalid)
        if (l != null) pairWith(l)
    }

    val t = target
    if (t != null) {
        SignIn(t, onBack = { target = null }, onPaired = { finish(t, it) })
        return
    }

    Column(Modifier.fillMaxSize().safeDrawingPadding().verticalScroll(rememberScrollState()).padding(24.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
        Icon(Icons.Default.Storage, null, Modifier.size(48.dp), tint = MaterialTheme.colorScheme.primary)
        Text(stringResource(R.string.welcome_title), style = MaterialTheme.typography.headlineMedium)
        Text(stringResource(R.string.welcome_text), style = MaterialTheme.typography.bodyMedium)
        Button(onClick = { scan.launch(ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE).setPrompt(appRes.getString(R.string.scan_prompt)).setBeepEnabled(false).setOrientationLocked(true)) },
            modifier = Modifier.fillMaxWidth(), enabled = !busy) {
            Icon(Icons.Default.QrCodeScanner, null)
            Spacer(Modifier.size(8.dp))
            Text(stringResource(R.string.scan_qr))
        }
        Text(stringResource(R.string.scan_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (found.isNotEmpty()) {
            Text(stringResource(R.string.found_on_network), style = MaterialTheme.typography.titleMedium)
            Card {
                found.forEach { n ->
                    ListItem(headlineContent = { Text(n.name) }, supportingContent = { Text(n.url) }, leadingContent = { Icon(Icons.Default.Storage, null) },
                        modifier = Modifier.clickable(enabled = !busy) { address = n.url; check(n.url) })
                }
            }
        } else {
            Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
                Spacer(Modifier.size(8.dp))
                Text(stringResource(R.string.searching), style = MaterialTheme.typography.bodySmall)
            }
        }
        OutlinedTextField(address, { address = it }, label = { Text(stringResource(R.string.address)) }, placeholder = { Text("192.168.1.20, nas.example.com…") },
            supportingText = { Text(stringResource(R.string.address_hint)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Go))
        error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
        Button(onClick = {
            val url = normalizeAddress(address)
            if (url == null) error = appRes.getString(R.string.err_address) else check(url)
        }, enabled = !busy && address.isNotBlank(), modifier = Modifier.fillMaxWidth()) {
            if (busy) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) else Text(stringResource(R.string.connect))
        }
    }

    cert?.let { (url, info) ->
        TrustDialog(info, onTrust = { caOnly -> cert = null; target = Target(url, if (caOnly) "" else info.fingerprint, caOnly) }, onCancel = { cert = null })
    }
}

/** Trust on first use: the fingerprint, to compare with the web interface (account menu → Mobile devices). */
@Composable
fun TrustDialog(info: CertInfo, onTrust: (caOnly: Boolean) -> Unit, onCancel: () -> Unit) {
    AlertDialog(
        onDismissRequest = onCancel,
        icon = { Icon(Icons.Default.Warning, null) },
        title = { Text(stringResource(R.string.trust_title)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(stringResource(if (info.selfSigned) R.string.trust_self_signed else R.string.trust_unknown_ca))
                Text(stringResource(R.string.trust_fingerprint), style = MaterialTheme.typography.labelLarge)
                Text(info.fingerprint, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
                Text(stringResource(R.string.trust_details, info.subject, info.validUntil), style = MaterialTheme.typography.bodySmall)
                Text(stringResource(R.string.trust_compare), style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = { Button(onClick = { onTrust(false) }) { Text(stringResource(R.string.trust_accept)) } },
        dismissButton = { TextButton(onClick = onCancel) { Text(stringResource(R.string.cancel)) } },
    )
}

@Composable
private fun SignIn(t: Target, onBack: () -> Unit, onPaired: (PairResponse) -> Unit) {
    val ctx = LocalContext.current
    val appRes = androidx.compose.ui.platform.LocalResources.current
    val scope = rememberCoroutineScope()
    var user by remember { mutableStateOf("") }
    var pass by remember { mutableStateOf("") }
    var code by remember { mutableStateOf("") }
    var needCode by remember { mutableStateOf(false) }
    var name by remember { mutableStateOf(Graph.deviceModel) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    Column(Modifier.fillMaxSize().safeDrawingPadding().verticalScroll(rememberScrollState()).padding(24.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(stringResource(R.string.sign_in), style = MaterialTheme.typography.headlineMedium)
        Text(t.url, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (t.url.startsWith("http://")) Text(stringResource(R.string.warn_http), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
        OutlinedTextField(user, { user = it }, label = { Text(stringResource(R.string.username)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
        OutlinedTextField(pass, { pass = it }, label = { Text(stringResource(R.string.password)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password))
        if (needCode) OutlinedTextField(code, { code = it }, label = { Text(stringResource(R.string.two_factor_code)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number))
        OutlinedTextField(name, { name = it }, label = { Text(stringResource(R.string.device_name)) }, singleLine = true, modifier = Modifier.fillMaxWidth())
        error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
        Button(enabled = !busy && user.isNotBlank() && pass.isNotEmpty(), modifier = Modifier.fillMaxWidth(), onClick = {
            busy = true
            error = null
            scope.launch {
                try {
                    val r = withContext(Dispatchers.IO) {
                        Api(t.url, Graph.client(ServerConfig(t.url, t.fingerprint, t.caOnly)), null)
                            .pairWithPassword(user.trim(), pass, code.trim(), name, Graph.deviceModel, BuildConfig.VERSION_NAME)
                    }
                    onPaired(r)
                } catch (e: ApiException) {
                    if (e.twoFactor) needCode = true
                    error = if (e.twoFactor && code.isBlank()) appRes.getString(R.string.two_factor_needed) else errorText(ctx, e)
                } catch (e: Exception) {
                    error = errorText(ctx, e)
                } finally {
                    busy = false
                }
            }
        }) { if (busy) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) else Text(stringResource(R.string.sign_in)) }
        OutlinedButton(onClick = onBack, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.back)) }
        Spacer(Modifier.height(8.dp))
        Text(stringResource(R.string.sign_in_note), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

