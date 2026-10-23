package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.data.hostPort
import io.github.nathplay15.strataos.data.normalizeAddress
import io.github.nathplay15.strataos.data.parsePairLink
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AddressTest {
    @Test fun addresses() {
        mapOf(
            "192.168.1.20" to "https://192.168.1.20:7443",
            "192.168.1.20:7070" to "http://192.168.1.20:7070",
            "nas.local" to "https://nas.local:7443",
            "strata.tailnet-abc.ts.net" to "https://strata.tailnet-abc.ts.net:7443",
            "10.8.0.1:7443" to "https://10.8.0.1:7443",
            "https://nas.example.com" to "https://nas.example.com",
            "https://nas.example.com:443/" to "https://nas.example.com",
            "HTTP://nas:8080" to "http://nas:8080",
            "[fd00::5]" to "https://[fd00::5]:7443",
            "https://[fd00::5]:7443" to "https://[fd00::5]:7443",
        ).forEach { (input, want) -> assertEquals(input, want, normalizeAddress(input)) }
        listOf("", "nas local", "ftp://nas", "https://nas/path", "https://u:p@nas", "https://nas?x=1").forEach { assertNull(it, normalizeAddress(it)) }
        assertEquals("fd00::5" to 7443, hostPort("https://[fd00::5]:7443"))
        assertEquals("nas.example.com" to 443, hostPort("https://nas.example.com"))
    }

    @Test fun pairLinks() {
        val fp = "84:CB:05:95:4E:A7:A8:04:29:DF:EE:53:9F:73:C8:49:BA:86:C0:52:FC:A7:37:13:8B:40:2E:9C:93:0A:8D:07"
        val l = parsePairLink("strataos://pair?url=https%3A%2F%2F192.168.1.20%3A7443&fp=${fp.replace(":", "%3A")}&code=0123456789abcdef0123456789abcdef&user=alice")!!
        assertEquals("https://192.168.1.20:7443", l.url)
        assertEquals(fp, l.fingerprint)
        assertEquals("alice", l.user)
        assertNull(parsePairLink("https://example.com/pair?url=x"))
        assertNull(parsePairLink("strataos://pair?url=https%3A%2F%2Fnas&code=short"))
        assertNull(parsePairLink("strataos://pair?url=ftp%3A%2F%2Fnas&code=0123456789abcdef0123456789abcdef"))
        assertNull(parsePairLink("not a url at all"))
    }
}
