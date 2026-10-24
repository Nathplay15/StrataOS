package io.github.nathplay15.strataos.ui.web

import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.net.http.SslCertificate
import android.net.http.SslError
import android.os.Build
import android.util.Base64
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import android.webkit.SslErrorHandler
import android.webkit.URLUtil
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.viewinterop.AndroidView
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.fingerprint
import io.github.nathplay15.strataos.net.webSession
import io.github.nathplay15.strataos.ui.ErrorPane
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import java.io.ByteArrayInputStream
import java.io.IOException
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

// The complete web interface of the NAS, inside the app, for everything the
// app does not show natively. The app trades its device token for a one-time
// code and opens it: the NAS answers with an ordinary session cookie, tied to
// this phone (revoking the phone ends it; changing the account's password,
// two-factor or passkeys stays refused, as from the app itself).
//
// Certificates: the WebView checks them like a browser. The NAS's own
// self-signed certificate fails that check: it is accepted only when its
// SHA-256 is the one the app pinned at pairing (and the page is the NAS's),
// like PinningTrustManager does for the app's own requests. Anything else
// is refused.

/** Whether an SSL error of the WebView may be overridden: the pinned certificate of the NAS itself. */
fun sslOverride(presented: String?, pin: String, caOnly: Boolean, pageHost: String?, nasHost: String): Boolean =
    !caOnly && pin.isNotEmpty() && presented != null && presented == pin && pageHost != null && pageHost.equals(nasHost, ignoreCase = true)

/** Same NAS: same scheme, host and port as the paired address (other ports are its apps: opened outside). */
fun sameOrigin(u: HttpUrl?, base: HttpUrl): Boolean = u != null && u.scheme == base.scheme && u.host.equals(base.host, true) && u.port == base.port

fun x509Of(c: SslCertificate): X509Certificate? {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) return c.x509Certificate
    val der = SslCertificate.saveState(c).getByteArray("x509-certificate") ?: return null
    return runCatching { CertificateFactory.getInstance("X.509").generateCertificate(ByteArrayInputStream(der)) as X509Certificate }.getOrNull()
}

/**
 * Saves the files the web interface builds in the page (blob: links with a
 * download name: VPN configurations, exports) through the app; other
 * downloads come through the DownloadListener.
 */
private const val BLOB_HOOK = """
(function(){
  if (window.__strataHook) return; window.__strataHook = true;
  function save(a){
    fetch(a.href).then(function(r){return r.blob();}).then(function(b){
      var fr = new FileReader();
      fr.onload = function(){ StrataApp.save(a.download || 'download', b.type || 'application/octet-stream', String(fr.result).split(',')[1] || ''); };
      fr.readAsDataURL(b);
    });
  }
  var click = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function(){ if (this.href && this.href.indexOf('blob:') === 0) { save(this); return; } return click.apply(this, arguments); };
  document.addEventListener('click', function(e){
    var a = e.target && e.target.closest ? e.target.closest('a') : null;
    if (a && a.href && a.href.indexOf('blob:') === 0) { e.preventDefault(); e.stopPropagation(); save(a); }
  }, true);
})();
"""

private class Pending(val name: String, val mime: String, val data: ByteArray? = null, val url: String? = null)

@SuppressLint("SetJavaScriptEnabled") // the NAS's own web interface needs it; the page is checked (origin, certificate)
@Composable
fun WebArea(back: () -> Unit) {
    val ctx = LocalContext.current
    val res = LocalResources.current
    val cfg = Graph.prefs.server
    val base = remember(cfg?.url) { cfg?.url?.trimEnd('/')?.toHttpUrlOrNull() }
    var start by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<Throwable?>(null) }
    var attempt by remember { mutableIntStateOf(0) }
    var progress by remember { mutableIntStateOf(0) }
    var web by remember { mutableStateOf<WebView?>(null) }
    val actions = rememberActions()

    LaunchedEffect(attempt) {
        error = null
        start = try {
            val code = withContext(Dispatchers.IO) { api().webSession() }
            base!!.newBuilder().encodedPath(code.path).build().toString()
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            error = e
            null
        }
    }

    // Uploads: the page's <input type=file>.
    var fileCallback by remember { mutableStateOf<ValueCallback<Array<Uri>>?>(null) }
    val pickFiles = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
        fileCallback?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(r.resultCode, r.data))
        fileCallback = null
    }
    // Downloads: saved where the user chooses.
    var pending by remember { mutableStateOf<Pending?>(null) }
    val saveAs = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("*/*")) { uri ->
        val p = pending ?: return@rememberLauncherForActivityResult
        pending = null
        if (uri != null) actions.run(done = res.getString(R.string.b_saved)) {
            ctx.contentResolver.openOutputStream(uri)?.use { out ->
                if (p.data != null) out.write(p.data)
                else {
                    val a = api()
                    val req = okhttp3.Request.Builder().url(p.url!!).header("Cookie", CookieManager.getInstance().getCookie(p.url) ?: "").build()
                    a.http.newCall(req).execute().use { r ->
                        if (!r.isSuccessful) throw io.github.nathplay15.strataos.net.Api.apiError(r.code, r.body.string())
                        r.body.byteStream().copyTo(out, 64 * 1024)
                    }
                }
            } ?: throw IOException("cannot write the file")
        }
    }
    LaunchedEffect(pending) { pending?.let { saveAs.launch(it.name) } }

    // System back goes back in the pages first; the arrow leaves.
    val pageBack: () -> Unit = { val w = web; if (w != null && w.canGoBack()) w.goBack() else back() }
    DisposableEffect(Unit) { onDispose { web?.destroy() } }

    AreaScreen(stringResource(R.string.area_web), onBack = back, systemBack = pageBack, actions = {
        IconButton(onClick = { if (start == null) attempt++ else web?.reload() }) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) }
    }) { pad ->
        Box(Modifier.padding(pad).fillMaxSize()) {
            val e = error
            when {
                cfg == null || base == null -> ErrorPane(IllegalStateException(res.getString(R.string.web_not_paired))) { back() }
                e != null -> ErrorPane(e) { attempt++ }
                start != null -> AndroidView(modifier = Modifier.fillMaxSize(), factory = { c ->
                    WebView(c).apply {
                        CookieManager.getInstance().setAcceptCookie(true)
                        settings.javaScriptEnabled = true
                        settings.domStorageEnabled = true
                        settings.allowFileAccess = false
                        settings.allowContentAccess = false
                        settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                        settings.setSupportMultipleWindows(false)
                        settings.userAgentString = settings.userAgentString + " StrataOS-Android"
                        addJavascriptInterface(object {
                            @JavascriptInterface
                            fun save(name: String, mime: String, b64: String) {
                                val data = runCatching { Base64.decode(b64, Base64.DEFAULT) }.getOrNull() ?: return
                                post { pending = Pending(URLUtil.guessFileName(null, "attachment; filename=\"$name\"", mime).let { if (name.isNotBlank()) name else it }, mime, data) }
                            }
                        }, "StrataApp")
                        webViewClient = object : WebViewClient() {
                            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, err: SslError) {
                                val presented = err.certificate?.let { x509Of(it) }?.let { fingerprint(it) }
                                val host = err.url?.toHttpUrlOrNull()?.host
                                if (sslOverride(presented, cfg.fingerprint, cfg.caOnly, host, base.host)) handler.proceed() else handler.cancel()
                            }

                            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                                val u = request.url.toString().toHttpUrlOrNull()
                                if (sameOrigin(u, base)) return false
                                // The NAS's apps (other ports) and outside links open in the browser.
                                try { c.startActivity(Intent(Intent.ACTION_VIEW, request.url).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) } catch (_: ActivityNotFoundException) {}
                                return true
                            }

                            override fun onPageFinished(view: WebView, url: String?) {
                                if (sameOrigin(url?.toHttpUrlOrNull(), base)) view.evaluateJavascript(BLOB_HOOK, null)
                            }
                        }
                        webChromeClient = object : WebChromeClient() {
                            override fun onProgressChanged(view: WebView, p: Int) { progress = p }
                            override fun onShowFileChooser(view: WebView, cb: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                                fileCallback?.onReceiveValue(null)
                                fileCallback = cb
                                return try {
                                    pickFiles.launch(params.createIntent().apply { if (params.mode == FileChooserParams.MODE_OPEN_MULTIPLE) putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true) })
                                    true
                                } catch (_: ActivityNotFoundException) {
                                    fileCallback = null
                                    false
                                }
                            }
                        }
                        setDownloadListener { url, _, disposition, mime, _ ->
                            val u = url.toHttpUrlOrNull()
                            if (sameOrigin(u, base)) pending = Pending(URLUtil.guessFileName(url, disposition, mime), mime ?: "application/octet-stream", url = url)
                        }
                        web = this
                        loadUrl(start!!)
                    }
                })
            }
            if (start != null && progress in 1..99) LinearProgressIndicator(progress = { progress / 100f }, Modifier.fillMaxWidth().align(Alignment.TopCenter))
        }
    }
}
