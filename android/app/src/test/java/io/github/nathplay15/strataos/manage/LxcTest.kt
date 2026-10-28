package io.github.nathplay15.strataos.manage

import io.github.nathplay15.strataos.net.json
import io.github.nathplay15.strataos.net.manage.Container
import io.github.nathplay15.strataos.net.manage.LxcImage
import io.github.nathplay15.strataos.net.manage.LxcList
import io.github.nathplay15.strataos.net.manage.LxcSettings
import io.github.nathplay15.strataos.net.manage.LxcStats
import io.github.nathplay15.strataos.net.manage.filterImages
import io.github.nathplay15.strataos.net.manage.lxcActions
import io.github.nathplay15.strataos.net.manage.lxcCpuPercent
import io.github.nathplay15.strataos.net.manage.mountTarget
import io.github.nathplay15.strataos.net.manage.settingsOf
import io.github.nathplay15.strataos.net.manage.validCidr
import io.github.nathplay15.strataos.net.manage.validLxcName
import io.github.nathplay15.strataos.term.TermKeys
import io.github.nathplay15.strataos.term.TermScreen
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class LxcTest {
    // An answer of GET /api/lxc as the NAS writes it (api/h_lxc.go).
    private val listJson = """
        {"installed":true,"bridges":["br0"],"natBridge":"lxcbr0","arch":"amd64","containers":[{"version":1,"name":"web","description":"site",
        "created":"2026-10-01T10:00:00Z","image":{"distro":"debian","release":"trixie","arch":"amd64"},"idmap":{"base":100000,"count":65536,"uids":[1001]},
        "storage":{"backend":"btrfs","location":"/srv/strata/main","base":"/srv/strata/main/.strata-lxc/web","sizeGb":8},"cpus":1.5,"memoryMb":1024,
        "network":{"mode":"bridge","bridge":"br0","mac":"00:16:3e:00:00:01"},"mounts":[{"share":"Media","source":"/srv/strata/main/Media","target":"/mnt/media","readOnly":true}],
        "features":{"nesting":true},"autostart":true,"state":"running","pid":42,"ips":["192.168.1.50"],"snapshots":2,"memoryBytes":123456,
        "mapUsers":["alice"],"mapGroups":[]}]}
    """.trimIndent()

    @Test fun parsesTheListAndBuildsSettings() {
        val l = json.decodeFromString(LxcList.serializer(), listJson)
        val c = l.containers.single()
        assertTrue(c.running)
        assertEquals("br0", c.network.bridge)
        val s = settingsOf(c)
        assertEquals(8, s.sizeGb)
        assertEquals(1.5, s.cpus, 0.0)
        assertEquals(listOf("alice"), s.mapUsers)
        // The NAS fills in the source of each mount: the settings send only the share.
        assertEquals("", s.mounts.single().source)
        assertTrue(s.mounts.single().readOnly)
        val o = json.encodeToJsonElement(LxcSettings.serializer(), s).jsonObject
        assertTrue("mapUsers" in o && "network" in o && "autostart" in o)
    }

    @Test fun actionsByState() {
        assertEquals(listOf("stop", "restart", "kill"), lxcActions(Container(state = "running")))
        assertEquals(listOf("start"), lxcActions(Container(state = "stopped")))
        assertEquals(emptyList<String>(), lxcActions(Container(state = "stopped", missing = true)))
        assertEquals(listOf("kill"), lxcActions(Container(state = "frozen")))
    }

    @Test fun validation() {
        assertTrue(validLxcName("web-1"))
        assertFalse(validLxcName("Web"))
        assertFalse(validLxcName("-web"))
        assertFalse(validLxcName("templates"))
        assertTrue(validCidr(""))
        assertTrue(validCidr("192.168.1.50/24"))
        assertFalse(validCidr("192.168.1.50"))
        assertFalse(validCidr("300.1.1.1/24"))
        assertEquals("/mnt/my-photos", mountTarget("My Photos"))
    }

    @Test fun imagesAndCpu() {
        val imgs = listOf(LxcImage("debian", "trixie", label = "Debian 13", recommended = true), LxcImage("alpine", "3.22", label = "Alpine 3.22"))
        assertEquals(listOf("debian"), filterImages(imgs, "", false).map { it.distro })
        assertEquals(2, filterImages(imgs, "", true).size)
        assertEquals(listOf("alpine"), filterImages(imgs, "alp", false).map { it.distro })
        val a = LxcStats(time = "2026-10-01T10:00:00Z", state = "running", cpuUsec = 1_000_000)
        val b = LxcStats(time = "2026-10-01T10:00:02Z", state = "running", cpuUsec = 2_000_000)
        assertEquals(50.0, lxcCpuPercent(a, b)!!, 0.01) // 1 s of CPU in 2 s: half a core
        assertNull(lxcCpuPercent(null, b))
    }
}

class TermTest {
    @Test fun textCursorAndWrap() {
        val t = TermScreen(10, 3)
        t.feed("hello\r\nworld")
        assertEquals("hello\nworld", t.text())
        assertEquals(5, t.x)
        t.feed("\r\n0123456789AB")
        // Line 3 wrapped: the screen scrolled one line, "hello" went to the scrollback.
        assertEquals("world\n0123456789\nAB", t.text())
        assertEquals("hello", String(CharArray(10) { t.scrollback.last()[it].c }).trimEnd())
    }

    @Test fun csiMovesAndErases() {
        val t = TermScreen(20, 5)
        t.feed("abcdef\u001b[3D\u001b[K")
        assertEquals("abc", t.text())
        t.feed("\u001b[2J\u001b[3;5HX")
        assertEquals("\n\n    X", t.text())
        t.feed("\u001b[1;1Hline1\u001b[2;1Hline2\u001b[1;1H\u001b[L")
        assertEquals("\nline1\nline2\n    X", t.text())
        t.feed("\u001b[M")
        assertEquals("line1\nline2\n    X", t.text())
        t.feed("\u001b[1;3H\u001b[2P")
        assertEquals("li1\nline2\n    X", t.text())
    }

    @Test fun utf8ColoursAndModes() {
        val t = TermScreen(20, 3)
        t.feed("é€\u001b[1;31mR\u001b[0m\u001b[38;5;200mP\u001b[48;2;1;2;3mT")
        assertEquals("é€RPT", t.text())
        val row = t.line(0)
        assertEquals(1, row[2].fg)
        assertEquals(TermScreen.BOLD, row[2].a)
        assertEquals(200, row[3].fg)
        assertEquals(TermScreen.RGB or 0x010203, row[4].bg)
        // UTF-8 split across two packets.
        val b = "ü".toByteArray()
        t.feed(b, 0, 1)
        t.feed(b, 1, 1)
        assertTrue(t.text().endsWith("ü"))
        // Alternate screen (editors, pagers) and application cursor keys.
        t.feed("\u001b[?1049h\u001b[?1h\u001b[Hvim")
        assertEquals("vim", t.text())
        assertTrue(t.appCursor)
        assertEquals("\u001bOA", TermKeys.named("up", t.appCursor))
        t.feed("\u001b[?1049l\u001b[?1l")
        assertTrue(t.text().startsWith("é€RPT"))
        assertEquals("\u001b[A", TermKeys.named("up", t.appCursor))
        // Titles (OSC) are read and ignored.
        t.feed("\u001b]0;title\u0007ok")
        assertTrue(t.text().contains("ok"))
        assertFalse(t.text().contains("title"))
    }

    @Test fun answersQueriesAndScrollRegions() {
        var answer = ""
        val t = TermScreen(10, 4) { answer += it }
        t.feed("\u001b[2;3H\u001b[6n")
        assertEquals("\u001b[2;3R", answer)
        t.feed("\u001b[2;3r\u001b[1;1Ha\r\nb\r\nc\r\nd\r\ne")
        // Only lines 2 and 3 scroll: "a" stays at the top.
        assertEquals("a\nd\ne", t.text())
        t.resize(5, 2)
        assertEquals(5, t.cols)
        assertEquals(2, t.rows)
    }

    @Test fun keys() {
        assertEquals("\u0003", TermKeys.ctrl('c'))
        assertEquals("\u0004", TermKeys.ctrl('D'))
        assertEquals("\u001b", TermKeys.ctrl('['))
        assertEquals("\t", TermKeys.named("tab", false))
        assertEquals("\u001b[3~", TermKeys.named("delete", false))
    }
}
