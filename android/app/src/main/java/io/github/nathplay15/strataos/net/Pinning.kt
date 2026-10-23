package io.github.nathplay15.strataos.net

import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.SSLSession
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager
import okhttp3.internal.tls.OkHostnameVerifier

/** SHA-256 of a certificate as the NAS shows it: "AB:CD:…" (upper case, colons). */
fun fingerprint(cert: X509Certificate): String =
    MessageDigest.getInstance("SHA-256").digest(cert.encoded).joinToString(":") { "%02X".format(it) }

/** Normalizes a fingerprint typed or scanned in any common format. */
fun normalizeFingerprint(v: String): String {
    val hex = v.filter { it.isLetterOrDigit() }.uppercase()
    if (hex.length != 64 || hex.any { it !in "0123456789ABCDEF" }) return ""
    return hex.chunked(2).joinToString(":")
}

/** The certificate changed since it was pinned and no public CA vouches for it. */
class CertificateChangedException(val presented: String, val pinned: String) :
    CertificateException("The NAS certificate changed (now $presented)")

/** A certificate nobody vouches for yet: the user must look at it (trust on first use). */
class UntrustedCertificateException(val presented: String) :
    CertificateException("Certificate not trusted yet ($presented)")

/**
 * Trust for one NAS. A NAS uses a self-signed certificate by default: the app
 * pins its fingerprint once the user accepted it (shown at first connection or
 * carried by the QR code). A certificate from a public CA is accepted too
 * (Let's Encrypt renews it every few months, changing the fingerprint), unless
 * [caOnly] is set, in which case only CA-validated certificates are accepted
 * and the pin is ignored.
 */
class PinningTrustManager(
    private val pin: String?,
    private val caOnly: Boolean,
    private val system: X509TrustManager = systemTrustManager(),
) : X509TrustManager {
    override fun checkClientTrusted(chain: Array<out X509Certificate>, authType: String) =
        throw CertificateException("client certificates not supported")

    override fun checkServerTrusted(chain: Array<out X509Certificate>, authType: String) {
        if (chain.isEmpty()) throw CertificateException("empty certificate chain")
        val presented = fingerprint(chain[0])
        if (!caOnly && !pin.isNullOrEmpty() && presented == pin) return
        try {
            system.checkServerTrusted(chain, authType)
        } catch (e: CertificateException) {
            if (caOnly) throw e
            if (!pin.isNullOrEmpty()) throw CertificateChangedException(presented, pin)
            throw UntrustedCertificateException(presented)
        }
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = system.acceptedIssuers

    /** Host names are checked for CA certificates only: a pinned certificate is the NAS itself, whatever address reaches it (IP, VPN, Tailscale). */
    fun hostnameVerifier(): HostnameVerifier = PinnedHostnameVerifier(if (caOnly) null else pin)
}

class PinnedHostnameVerifier(private val pin: String?, private val fallback: HostnameVerifier = OkHostnameVerifier) : HostnameVerifier {
    override fun verify(hostname: String, session: SSLSession): Boolean {
        val leaf = runCatching { session.peerCertificates.firstOrNull() as? X509Certificate }.getOrNull()
        if (leaf != null && !pin.isNullOrEmpty() && fingerprint(leaf) == pin) return true
        return fallback.verify(hostname, session)
    }
}

fun systemTrustManager(): X509TrustManager {
    val f = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm())
    f.init(null as KeyStore?)
    return f.trustManagers.filterIsInstance<X509TrustManager>().first()
}
