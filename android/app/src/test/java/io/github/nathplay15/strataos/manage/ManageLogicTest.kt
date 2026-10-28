package io.github.nathplay15.strataos.manage

import io.github.nathplay15.strataos.net.FrigateEvent
import io.github.nathplay15.strataos.net.PasswordProblem
import io.github.nathplay15.strataos.net.Segment
import io.github.nathplay15.strataos.net.VpnPeer
import io.github.nathplay15.strataos.net.json
import io.github.nathplay15.strataos.net.mergeJson
import io.github.nathplay15.strataos.net.passwordProblem
import io.github.nathplay15.strataos.net.toReq
import io.github.nathplay15.strataos.net.userNameRe
import io.github.nathplay15.strataos.net.vmNameRe
import io.github.nathplay15.strataos.ui.fleet.fleetWebUrl
import io.github.nathplay15.strataos.ui.fleet.validFleetHost
import io.github.nathplay15.strataos.ui.nassettings.hhmm
import io.github.nathplay15.strataos.ui.surveillance.LIVE_SPEED_UP
import io.github.nathplay15.strataos.ui.surveillance.cameraUrlProblem
import io.github.nathplay15.strataos.ui.surveillance.catchUpSpeed
import io.github.nathplay15.strataos.ui.surveillance.clock
import io.github.nathplay15.strataos.ui.surveillance.dayStart
import io.github.nathplay15.strataos.ui.surveillance.filterEvents
import io.github.nathplay15.strataos.ui.surveillance.segmentAt
import io.github.nathplay15.strataos.ui.surveillance.spans
import io.github.nathplay15.strataos.ui.vms.cpuPercent
import io.github.nathplay15.strataos.ui.vms.powerActions
import io.github.nathplay15.strataos.ui.vms.vmFormProblems
import io.github.nathplay15.strataos.ui.vpn.ageMinutes
import io.github.nathplay15.strataos.ui.vpn.confName
import io.github.nathplay15.strataos.ui.vpn.validExpiry
import io.github.nathplay15.strataos.ui.web.sameOrigin
import io.github.nathplay15.strataos.ui.web.sslOverride
import java.time.ZoneOffset
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class TimelineTest {
    private val utc = ZoneOffset.UTC
    private val day = dayStart("2026-10-23", utc)!!
    private val segs = listOf(
        Segment("20261023-100000.mp4", "2026-10-23T10:00:00Z", "2026-10-23T10:10:00Z"),
        Segment("20261023-101000.mp4", "2026-10-23T10:10:00Z", "2026-10-23T10:20:00Z"),
        Segment("20261023-120000.mp4", "2026-10-23T12:00:00Z", "2026-10-23T12:05:00Z", open = true),
    )

    @Test fun pickTime() {
        val sp = spans(segs, day)
        assertEquals(3, sp.size)
        assertEquals(10f / 24, sp[0].from, 0.0001f)
        // 10:15:30 is 5 min 30 s into the second segment.
        assertEquals(segs[1] to 330, segmentAt(sp, day, (10 * 3600 + 15 * 60 + 30) / 86400f))
        // Before the first: from its start; in a gap after: nothing playable (the last one is still open).
        assertEquals(segs[0] to 0, segmentAt(sp, day, 0.1f))
        assertNull(segmentAt(sp, day, 11f / 24))
    }

    @Test fun clockText() {
        assertEquals("0:05", clock(5))
        assertEquals("10:00", clock(600))
        assertEquals("1:00:01", clock(3601))
    }

    @Test fun liveCatchUp() {
        assertEquals(1.15f, catchUpSpeed(LIVE_SPEED_UP + 1, 1f))
        assertEquals(1f, catchUpSpeed(200, 1.15f))
        assertEquals(1.15f, catchUpSpeed(1000, 1.15f)) // keeps going until close to live
    }

    @Test fun cameraUrls() {
        assertFalse(cameraUrlProblem("rtsp://192.168.1.20:554/stream1"))
        assertFalse(cameraUrlProblem("https://cam.local/video.mjpg"))
        assertTrue(cameraUrlProblem("file:///etc/shadow"))
        assertTrue(cameraUrlProblem("rtsp://h/a'b"))
        assertTrue(cameraUrlProblem("rtsp://h/a b"))
    }

    @Test fun eventFilter() {
        val ev = listOf(FrigateEvent("1", "person"), FrigateEvent("2", "car"), FrigateEvent("3", "person"))
        assertEquals(listOf("1", "3"), filterEvents(ev, "person").map { it.id })
        assertEquals(3, filterEvents(ev, "").size)
    }
}

class VmLogicTest {
    @Test fun cpu() {
        assertEquals(50, cpuPercent(1_000_000_000, 1_000_000_000, 2))
        assertEquals(100, cpuPercent(5_000_000_000, 1_000_000_000, 2))
        assertEquals(0, cpuPercent(10, 0, 1))
    }

    @Test fun form() {
        assertEquals(emptyList<String>(), vmFormProblems("web-1", 2, 2048, 32, "/srv/strata/main/vms", false, "/srv/strata/main/d.iso", "", ""))
        assertEquals(listOf("name", "memory", "storage", "iso"), vmFormProblems("a b", 2, 128, 32, "/tmp", false, "", "", ""))
        assertEquals(listOf("user"), vmFormProblems("c", 1, 512, 8, "/srv/strata/x", true, "", "debian13", "Bad User"))
        assertTrue(vmNameRe.matches("Win11_test.2"))
        assertFalse(vmNameRe.matches("-x"))
    }

    @Test fun actions() {
        val running = io.github.nathplay15.strataos.net.Vm(name = "a", state = "running")
        assertEquals(listOf("shutdown", "reboot", "suspend", "destroy"), powerActions(running).map { it.first })
        assertEquals(listOf("start"), powerActions(running.copy(state = "shut off")).map { it.first })
        assertEquals(emptyList<String>(), powerActions(running.copy(template = true)).map { it.first })
    }
}

class AccountsLogicTest {
    @Test fun passwords() {
        assertEquals(PasswordProblem.TooShort, passwordProblem("short"))
        assertEquals(PasswordProblem.Forbidden, passwordProblem("has:colon!"))
        assertNull(passwordProblem("correct horse"))
        assertNull(passwordProblem("ééééé")) // 10 bytes, like the NAS counts
    }

    @Test fun names() {
        assertTrue(userNameRe.matches("alice_2"))
        assertFalse(userNameRe.matches("Alice"))
        assertFalse(userNameRe.matches("2alice"))
    }
}

class VpnLogicTest {
    @Test fun expiry() {
        assertTrue(validExpiry(""))
        assertTrue(validExpiry("2026-12-31"))
        assertFalse(validExpiry("31/12/2026"))
    }

    @Test fun ages() {
        assertNull(ageMinutes(0))
        assertEquals(5L, ageMinutes(1000, (1000 + 300) * 1000L))
    }

    @Test fun files() {
        assertEquals("Pixel_de_Bob.conf", confName("Pixel de Bob"))
        assertEquals("strataos.conf", confName(""))
        assertEquals(15 + 5, confName("a-very-long-device-name").length)
    }

    @Test fun samePolicyBack() {
        val p = VpnPeer(id = "1", name = "phone", mode = "all", owner = "alice", expires = "2026-12-31T00:00:00Z", services = listOf("smb"))
        val r = p.toReq(disabled = true)
        assertEquals("all", r.mode); assertEquals("2026-12-31T00:00:00Z", r.expires); assertEquals(listOf("smb"), r.services); assertTrue(r.disabled)
    }
}

class SettingsLogicTest {
    @Test fun merge() {
        val base = json.decodeFromString(JsonObject.serializer(), """{"smtpPassword":"********","quietHours":{"enabled":false},"workgroup":"WG"}""")
        val m = mergeJson(base, mapOf("quietHours" to JsonPrimitive("x")))
        assertEquals("********", m["smtpPassword"]!!.jsonPrimitive.content) // masked secrets go back as they came
        assertEquals("WG", m["workgroup"]!!.jsonPrimitive.content)
        assertEquals("x", m["quietHours"]!!.jsonPrimitive.content)
    }

    @Test fun hours() {
        assertTrue(hhmm("22:30")); assertTrue(hhmm("07:00"))
        assertFalse(hhmm("7:00")); assertFalse(hhmm("24:00"))
    }

    @Test fun fleet() {
        assertTrue(validFleetHost("nas2.local"))
        assertFalse(validFleetHost("https://x"))
        assertEquals("https://192.168.1.3:7443/", fleetWebUrl("192.168.1.3"))
        assertEquals("https://[fd00::3]:7443/", fleetWebUrl("fd00::3"))
    }
}

class WebSessionLogicTest {
    private val pin = "AB:CD"

    @Test fun certificateOverride() {
        assertTrue(sslOverride("AB:CD", pin, false, "192.168.1.10", "192.168.1.10"))
        assertFalse(sslOverride("EF:01", pin, false, "192.168.1.10", "192.168.1.10")) // another certificate
        assertFalse(sslOverride("AB:CD", pin, true, "192.168.1.10", "192.168.1.10")) // CA only
        assertFalse(sslOverride("AB:CD", "", false, "192.168.1.10", "192.168.1.10")) // nothing pinned
        assertFalse(sslOverride("AB:CD", pin, false, "evil.example", "192.168.1.10")) // another site
        assertFalse(sslOverride(null, pin, false, "192.168.1.10", "192.168.1.10"))
    }

    @Test fun origins() {
        val base = "https://nas.local:7443".toHttpUrl()
        assertTrue(sameOrigin("https://nas.local:7443/files?x=1".toHttpUrl(), base))
        assertFalse(sameOrigin("https://nas.local:8096/".toHttpUrl(), base))
        assertFalse(sameOrigin("http://nas.local:7443/".toHttpUrl(), base))
        assertFalse(sameOrigin(null, base))
    }
}
