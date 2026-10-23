package io.github.nathplay15.strataos.net

import java.net.InetSocketAddress
import java.security.cert.X509Certificate
import java.text.DateFormat
import javax.net.ssl.SNIHostName
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
import javax.net.ssl.X509TrustManager

/** What the user sees before trusting a certificate. */
data class CertInfo(
    val fingerprint: String,
    val subject: String,
    val issuer: String,
    val validUntil: String,
    val selfSigned: Boolean,
    val publiclyTrusted: Boolean,
)

/**
 * Fetches the certificate of a NAS without trusting it: a bare TLS handshake
 * that sends nothing else, so the user can compare the fingerprint with the
 * one shown in the NAS web interface before anything (password, token) is sent.
 */
object CertProbe {
    fun probe(host: String, port: Int, timeoutMs: Int = 8000): CertInfo {
        var captured: Array<X509Certificate> = emptyArray()
        val capture = object : X509TrustManager {
            override fun checkClientTrusted(chain: Array<out X509Certificate>, authType: String) = Unit
            override fun checkServerTrusted(chain: Array<out X509Certificate>, authType: String) {
                @Suppress("UNCHECKED_CAST")
                captured = chain as Array<X509Certificate>
            }
            override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
        }
        val ctx = SSLContext.getInstance("TLS")
        ctx.init(null, arrayOf(capture), null)
        (ctx.socketFactory.createSocket() as SSLSocket).use { s ->
            if (host.any { it.isLetter() } && !host.contains(':')) {
                s.sslParameters = s.sslParameters.apply { serverNames = listOf(SNIHostName(host)) }
            }
            s.soTimeout = timeoutMs
            s.connect(InetSocketAddress(host, port), timeoutMs)
            s.startHandshake()
        }
        val leaf = captured.firstOrNull() ?: error("no certificate")
        val trusted = runCatching { systemTrustManager().checkServerTrusted(captured, "RSA"); true }.getOrDefault(false) ||
            runCatching { systemTrustManager().checkServerTrusted(captured, "ECDHE_ECDSA"); true }.getOrDefault(false)
        return CertInfo(
            fingerprint = fingerprint(leaf),
            subject = leaf.subjectX500Principal.name,
            issuer = leaf.issuerX500Principal.name,
            validUntil = DateFormat.getDateInstance(DateFormat.MEDIUM).format(leaf.notAfter),
            selfSigned = leaf.subjectX500Principal == leaf.issuerX500Principal,
            publiclyTrusted = trusted,
        )
    }
}
