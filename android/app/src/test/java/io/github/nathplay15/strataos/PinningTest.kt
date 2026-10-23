package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.net.CertificateChangedException
import io.github.nathplay15.strataos.net.PinnedHostnameVerifier
import io.github.nathplay15.strataos.net.PinningTrustManager
import io.github.nathplay15.strataos.net.UntrustedCertificateException
import io.github.nathplay15.strataos.net.fingerprint
import io.github.nathplay15.strataos.net.normalizeFingerprint
import java.security.cert.CertificateException
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.SSLSession
import javax.net.ssl.X509TrustManager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.lang.reflect.Proxy

class PinningTest {
    private fun cert(name: String): X509Certificate =
        javaClass.classLoader!!.getResourceAsStream(name).use { CertificateFactory.getInstance("X.509").generateCertificate(it) as X509Certificate }

    private val nas = cert("nas.pem")
    private val other = cert("other.pem")
    private val nasFp = "84:CB:05:95:4E:A7:A8:04:29:DF:EE:53:9F:73:C8:49:BA:86:C0:52:FC:A7:37:13:8B:40:2E:9C:93:0A:8D:07"

    /** Stands for the system CAs: trusts only [trusted]. */
    private fun system(vararg trusted: X509Certificate) = object : X509TrustManager {
        override fun checkClientTrusted(chain: Array<out X509Certificate>, authType: String) = Unit
        override fun checkServerTrusted(chain: Array<out X509Certificate>, authType: String) {
            if (chain[0] !in trusted) throw CertificateException("not trusted")
        }
        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }

    @Test fun fingerprintMatchesOpenssl() {
        assertEquals(nasFp, fingerprint(nas))
        assertEquals(nasFp, normalizeFingerprint(nasFp.replace(":", "").lowercase()))
        assertEquals("", normalizeFingerprint("12:34"))
        assertEquals("", normalizeFingerprint("ZZ".repeat(32)))
    }

    @Test fun pinnedCertificateAccepted() {
        PinningTrustManager(nasFp, false, system()).checkServerTrusted(arrayOf(nas), "ECDHE_ECDSA")
    }

    @Test fun otherCertificateRefusedWithItsFingerprint() {
        try {
            PinningTrustManager(nasFp, false, system()).checkServerTrusted(arrayOf(other), "ECDHE_ECDSA")
            fail("accepted")
        } catch (e: CertificateChangedException) {
            assertEquals(fingerprint(other), e.presented)
            assertEquals(nasFp, e.pinned)
        }
    }

    @Test fun publicCaStillAcceptedWhenPinned() {
        // Let's Encrypt renewals change the fingerprint: a CA-valid certificate passes.
        PinningTrustManager(nasFp, false, system(other)).checkServerTrusted(arrayOf(other), "ECDHE_ECDSA")
    }

    @Test fun nothingPinnedYet() {
        try {
            PinningTrustManager(null, false, system()).checkServerTrusted(arrayOf(nas), "ECDHE_ECDSA")
            fail("accepted")
        } catch (e: UntrustedCertificateException) {
            assertEquals(nasFp, e.presented)
        }
    }

    @Test fun caOnlyIgnoresThePin() {
        try {
            PinningTrustManager(nasFp, true, system()).checkServerTrusted(arrayOf(nas), "ECDHE_ECDSA")
            fail("pinned self-signed certificate accepted in CA-only mode")
        } catch (e: CertificateException) {
            assertFalse(e is CertificateChangedException)
        }
        PinningTrustManager(nasFp, true, system(nas)).checkServerTrusted(arrayOf(nas), "ECDHE_ECDSA")
    }

    private fun session(c: X509Certificate) = Proxy.newProxyInstance(javaClass.classLoader, arrayOf(SSLSession::class.java)) { _, m, _ ->
        if (m.name == "getPeerCertificates") arrayOf(c) else null
    } as SSLSession

    @Test fun hostnameSkippedOnlyForThePinnedCertificate() {
        val strict = HostnameVerifier { _, _ -> false }
        assertTrue(PinnedHostnameVerifier(nasFp, strict).verify("100.64.0.7", session(nas)))
        assertFalse(PinnedHostnameVerifier(nasFp, strict).verify("100.64.0.7", session(other)))
        assertFalse(PinnedHostnameVerifier(null, strict).verify("100.64.0.7", session(nas)))
    }
}
