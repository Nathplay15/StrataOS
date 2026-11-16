package io.github.nathplay15.strataos.ui.web

import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.http.SslError
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.SslErrorHandler
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.viewinterop.AndroidView
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.fingerprint
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/**
 * An application of the NAS (another port of the same host) opened inside the
 * app instead of the external browser: same certificate handling as the web
 * interface (the pinned certificate of the NAS is accepted, anything else is
 * refused) and the links leaving the NAS open in the browser.
 */
@SuppressLint("SetJavaScriptEnabled")
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AppWebView(title: String, url: String, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val cfg = Graph.prefs.server
    val base = remember(cfg?.url) { cfg?.url?.trimEnd('/')?.toHttpUrlOrNull() }
    var web by remember { mutableStateOf<WebView?>(null) }
    var progress by remember { mutableIntStateOf(0) }

    // System back goes back in the pages first; the arrow leaves.
    val pageBack: () -> Unit = { val w = web; if (w != null && w.canGoBack()) w.goBack() else onBack() }
    androidx.activity.compose.BackHandler(onBack = pageBack)
    DisposableEffect(Unit) { onDispose { web?.destroy() } }

    Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), topBar = {
        TopAppBar(
            title = { Text(title, maxLines = 1) },
            navigationIcon = { IconButton(onClick = pageBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
            actions = { IconButton(onClick = { web?.reload() }) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) } },
        )
    }) { pad ->
        Box(Modifier.padding(pad).fillMaxSize()) {
            if (cfg != null && base != null) {
                AndroidView(modifier = Modifier.fillMaxSize(), factory = { c ->
                    WebView(c).apply {
                        layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                        CookieManager.getInstance().setAcceptCookie(true)
                        settings.javaScriptEnabled = true
                        settings.domStorageEnabled = true
                        settings.allowFileAccess = false
                        settings.allowContentAccess = false
                        settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                        settings.setSupportMultipleWindows(false)
                        settings.userAgentString = settings.userAgentString + " StrataOS-Android"
                        webViewClient = object : WebViewClient() {
                            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, err: SslError) {
                                val presented = err.certificate?.let { x509Of(it) }?.let { fingerprint(it) }
                                val host = err.url?.toHttpUrlOrNull()?.host
                                if (sslOverride(presented, cfg.fingerprint, cfg.caOnly, host, base.host)) handler.proceed() else handler.cancel()
                            }

                            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                                val u = request.url.toString().toHttpUrlOrNull()
                                // Another port of the NAS stays in the app (the other apps);
                                // anything else opens in the browser.
                                if (u != null && u.scheme == base.scheme && u.host.equals(base.host, true)) return false
                                try { c.startActivity(Intent(Intent.ACTION_VIEW, request.url).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) } catch (_: ActivityNotFoundException) {}
                                return true
                            }
                        }
                        webChromeClient = object : WebChromeClient() {
                            override fun onProgressChanged(view: WebView, p: Int) { progress = p }
                        }
                        web = this
                        loadUrl(url)
                    }
                })
                if (progress in 1..99) LinearProgressIndicator(progress = { progress / 100f }, Modifier.fillMaxWidth().align(Alignment.TopCenter))
            }
        }
    }
}
