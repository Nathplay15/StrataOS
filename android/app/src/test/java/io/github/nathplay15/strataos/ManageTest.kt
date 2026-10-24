package io.github.nathplay15.strataos

import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Storage
import io.github.nathplay15.strataos.net.Album
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.AppCatalog
import io.github.nathplay15.strataos.net.CatalogApp
import io.github.nathplay15.strataos.net.Container
import io.github.nathplay15.strataos.net.ContainerUpdate
import io.github.nathplay15.strataos.net.Disk
import io.github.nathplay15.strataos.net.DockerUpdates
import io.github.nathplay15.strataos.net.Photo
import io.github.nathplay15.strataos.net.PlayOptions
import io.github.nathplay15.strataos.net.PlayPlan
import io.github.nathplay15.strataos.net.RaidArray
import io.github.nathplay15.strataos.net.json
import io.github.nathplay15.strataos.net.playUrl
import io.github.nathplay15.strataos.net.restartToSeek
import io.github.nathplay15.strataos.net.subtitleUrl
import io.github.nathplay15.strataos.ui.ManageSection
import io.github.nathplay15.strataos.ui.apps.filterApps
import io.github.nathplay15.strataos.ui.destinationOf
import io.github.nathplay15.strataos.ui.photos.groupByMonth
import io.github.nathplay15.strataos.ui.player.keepResume
import io.github.nathplay15.strataos.ui.player.resumePosition
import io.github.nathplay15.strataos.ui.storage.btrfsProfiles
import io.github.nathplay15.strataos.ui.storage.capacityOf
import io.github.nathplay15.strataos.ui.storage.eraseList
import io.github.nathplay15.strataos.ui.storage.freeDisks
import io.github.nathplay15.strataos.ui.storage.manualSnapshotName
import io.github.nathplay15.strataos.ui.storage.nextPermission
import io.github.nathplay15.strataos.ui.storage.raidLevels
import io.github.nathplay15.strataos.ui.storage.validStorageName
import io.github.nathplay15.strataos.ui.storage.volumeCandidates
import io.github.nathplay15.strataos.ui.storage.zfsLayouts
import io.github.nathplay15.strataos.ui.visibleSections
import java.time.YearMonth
import java.time.ZoneOffset
import java.util.Date
import okhttp3.OkHttpClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ManageTest {
    @Test fun sectionsForUsers() {
        val s = listOf(ManageSection("storage", 0, Icons.Default.Storage, true) {}, ManageSection("links", 0, Icons.Default.Storage, false) {},
            ManageSection("links", 0, Icons.Default.Storage, false) {})
        assertEquals(listOf("storage", "links"), visibleSections(s, admin = true).map { it.id })
        assertEquals(listOf("links"), visibleSections(s, admin = false).map { it.id })
    }

    @Test fun notificationTabs() {
        assertEquals("manage" to null, destinationOf("status"))
        assertEquals("files" to "backup", destinationOf("backup"))
        assertEquals("files" to "settings", destinationOf("settings"))
        assertEquals("files" to null, destinationOf("whatever"))
    }

    @Test fun updateStates() {
        val u = json.decodeFromString(DockerUpdates.serializer(),
            """{"checked":"2026-10-23T10:00:00Z","running":false,"containers":[{"id":"a1","name":"web","image":"nginx:latest","status":"available"},
               {"id":"b2","name":"db","image":"postgres:16","stack":"nextcloud","status":"current"},{"id":"c3","name":"x","image":"local","status":"unsupported"}],
               "stacks":{"nextcloud":false,"immich":true},"available":1,"extra":"ignored"}""")
        assertTrue(u.hasUpdate(Container(id = "a1")))
        assertFalse(u.hasUpdate(Container(id = "b2")))
        assertFalse(u.hasUpdate(Container(id = "zz")))
        assertTrue(u.stackHasUpdate("immich"))
        assertFalse(u.stackHasUpdate("nextcloud"))
        assertFalse(u.stackHasUpdate("missing"))
        assertEquals("unsupported", u.container("c3")?.status)
        assertEquals(ContainerUpdate(), DockerUpdates().container("a1") ?: ContainerUpdate())
    }

    @Test fun containersFromDockerJson() {
        val c = json.decodeFromString(Container.serializer(),
            """{"Id":"abcdef0123456789","Names":["/jellyfin"],"Image":"jellyfin/jellyfin","State":"running","Status":"Up 2 hours","Labels":{"com.docker.compose.project":"jellyfin"},"Ports":[{"PrivatePort":8096,"PublicPort":8096,"Type":"tcp"}]}""")
        assertEquals("jellyfin", c.name)
        assertEquals("jellyfin", c.stack)
        assertTrue(c.running)
        assertEquals("abcdef012345", Container(id = "abcdef0123456789").name)
    }

    @Test fun catalogFilter() {
        val c = AppCatalog(apps = listOf(CatalogApp("jellyfin", "Jellyfin", "Media", "Media server"), CatalogApp("immich", "Immich", "Photos", "Photo backup"),
            CatalogApp("nextcloud", "Nextcloud", "Productivity", "Files and calendar")), installed = mapOf("immich" to "immich"))
        assertEquals(3, filterApps(c, "", "").size)
        assertEquals(listOf("immich"), filterApps(c, "*", "").map { it.id })
        assertEquals(listOf("jellyfin"), filterApps(c, "Media", "").map { it.id })
        assertEquals(listOf("nextcloud"), filterApps(c, "", "CALENDAR").map { it.id })
    }

    @Test fun storagePlans() {
        val disks = listOf(
            Disk(name = "sda", path = "/dev/sda", size = 100, usage = "system", system = true),
            Disk(name = "sdb", path = "/dev/sdb", size = 4000, usage = "free", model = "WD Red", serial = "S1"),
            Disk(name = "sdc", path = "/dev/sdc", size = 2000, usage = "free"),
            Disk(name = "sdd", path = "/dev/sdd", size = 2000, usage = "raid", usedBy = "md0"),
        )
        assertEquals(listOf("/dev/sdb", "/dev/sdc"), freeDisks(disks).map { it.path })
        val raids = listOf(RaidArray(name = "md0", device = "/dev/md0", label = "data", level = "raid1", size = 2000, health = "healthy"),
            RaidArray(name = "md1", device = "/dev/md1", level = "raid5", fsType = "ext4", health = "healthy"))
        assertEquals(listOf("/dev/sdb", "/dev/sdc", "/dev/md0"), volumeCandidates(disks, raids).map { it.path })
        val r5 = raidLevels.first { it.id == "5" }
        assertEquals(0, capacityOf(r5, 2, 1000))
        assertEquals(2000, capacityOf(r5, 3, 1000))
        assertEquals(1000, capacityOf(zfsLayouts.first { it.id == "mirror" }, 2, 1000))
        assertEquals(2000, capacityOf(zfsLayouts.first { it.id == "raidz2" }, 4, 1000))
        assertEquals(1500, capacityOf(btrfsProfiles.first { it.id == "raid1" }, 3, 1000))
        assertTrue(validStorageName("tank_2"))
        assertFalse(validStorageName("-x"))
        assertFalse(validStorageName("a b"))
        assertFalse(validStorageName("a".repeat(33)))
        val lines = eraseList(freeDisks(disks)) { "${it}B" }
        assertEquals("/dev/sdb — WD Red — 4000B — S1", lines[0])
        assertTrue(manualSnapshotName(Date(0)).startsWith("manual-19"))
        assertEquals("ro", nextPermission(null))
        assertEquals("rw", nextPermission("ro"))
        assertEquals("none", nextPermission("rw"))
    }

    @Test fun photoMonths() {
        val jan = 1767225600L // 2026-01-01 00:00 UTC
        val groups = groupByMonth(listOf(Photo("/a", jan + 86400 * 40), Photo("/b", jan + 86400 * 35), Photo("/c", jan + 3600)), ZoneOffset.UTC)
        assertEquals(listOf(YearMonth.of(2026, 2), YearMonth.of(2026, 1)), groups.map { it.first })
        assertEquals(listOf("/a", "/b"), groups[0].second.map { it.path })
        assertEquals("c", Photo("/x/c").name.removePrefix(""))
        assertEquals(0, Album().count)
    }

    @Test fun playback() {
        val api = Api("https://nas.local:7443", OkHttpClient(), "stdev_x")
        val p = "/srv/strata/main/Films/a b.mkv"
        assertEquals("https://nas.local:7443/api/files/play?path=%2Fsrv%2Fstrata%2Fmain%2FFilms%2Fa%20b.mkv&audio=2&height=720&hevc=1&start=90.500",
            api.playUrl(p, PlayOptions(audio = 2, height = 720, hevc = true), 90.5).toString())
        assertEquals("https://nas.local:7443/api/files/download?path=%2Fsrv%2Fstrata%2Fmain%2FFilms%2Fa%20b.mkv", api.playUrl(p, PlayOptions(original = true), 30.0).toString())
        assertTrue(api.subtitleUrl(p, 3, 0.0).toString().endsWith("index=3"))
        assertTrue(PlayPlan(mode = "remux").restartToSeek(PlayOptions()))
        assertFalse(PlayPlan(mode = "native").restartToSeek(PlayOptions()))
        assertFalse(PlayPlan(mode = "transcode").restartToSeek(PlayOptions(original = true)))
        assertEquals(0.0, resumePosition(5.0, 3600.0), 0.0)
        assertEquals(1200.0, resumePosition(1200.0, 3600.0), 0.0)
        assertEquals(0.0, resumePosition(3590.0, 3600.0), 0.0)
        assertTrue(keepResume(600.0, 3600.0))
        assertFalse(keepResume(600.0, 60.0))
        assertFalse(keepResume(3500.0, 3600.0))
    }
}
