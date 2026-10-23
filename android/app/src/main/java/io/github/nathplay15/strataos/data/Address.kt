package io.github.nathplay15.strataos.data

import io.github.nathplay15.strataos.net.normalizeFingerprint
import java.net.URI
import java.net.URLDecoder

/** What the QR code of the web interface carries: strataos://pair?url=…&fp=…&code=…&user=… */
data class PairLink(val url: String, val fingerprint: String, val code: String, val user: String)

fun parsePairLink(text: String): PairLink? {
    val uri = runCatching { URI(text.trim()) }.getOrNull() ?: return null
    if (uri.scheme != "strataos" || uri.host != "pair") return null
    val q = (uri.rawQuery ?: return null).split("&").mapNotNull {
        val i = it.indexOf('=')
        if (i <= 0) null else URLDecoder.decode(it.substring(0, i), "UTF-8") to URLDecoder.decode(it.substring(i + 1), "UTF-8")
    }.toMap()
    val url = normalizeAddress(q["url"] ?: return null) ?: return null
    val code = q["code"]?.takeIf { it.length in 16..64 && it.all(Char::isLetterOrDigit) } ?: return null
    return PairLink(url, normalizeFingerprint(q["fp"] ?: ""), code, q["user"] ?: "")
}

/**
 * Turns what the user typed into a base URL. Without a scheme: HTTPS on the
 * StrataOS port 7443, or plain HTTP when the port is 7070 (the HTTP port).
 * With a scheme, the port is kept (an app domain behind a proxy uses 443).
 * Works for IP addresses (v4 and [v6]), local names, domains, Tailscale
 * (MagicDNS) names and WireGuard addresses alike.
 */
fun normalizeAddress(input: String): String? {
    var s = input.trim().trimEnd('/')
    if (s.isEmpty() || s.any { it.isWhitespace() }) return null
    val hasScheme = s.startsWith("https://", true) || s.startsWith("http://", true)
    if (s.contains("://") && !hasScheme) return null
    if (!hasScheme) s = "https://$s"
    val uri = runCatching { URI(s) }.getOrNull() ?: return null
    val host = uri.host ?: return null
    if (!uri.rawPath.isNullOrEmpty() && uri.rawPath != "/" || uri.rawQuery != null || uri.userInfo != null) return null
    var scheme = uri.scheme.lowercase()
    var port = uri.port
    if (!hasScheme) {
        if (port == 7070) scheme = "http" else if (port == -1) port = 7443
    }
    val h = if (host.contains(':') && !host.startsWith("[")) "[$host]" else host
    val defaultPort = if (scheme == "https") 443 else 80
    return if (port == -1 || port == defaultPort) "$scheme://$h" else "$scheme://$h:$port"
}

/** Host and port of a base URL, for the certificate probe. */
fun hostPort(url: String): Pair<String, Int> {
    val u = URI(url)
    val port = if (u.port != -1) u.port else if (u.scheme == "https") 443 else 80
    return u.host.trim('[', ']') to port
}
