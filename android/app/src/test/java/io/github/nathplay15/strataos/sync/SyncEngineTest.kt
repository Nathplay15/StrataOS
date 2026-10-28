package io.github.nathplay15.strataos.sync

import java.io.File
import java.nio.file.Files
import java.time.LocalDateTime
import java.time.ZoneOffset
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

// The cases of desktop/internal/engine/engine_test.go, on the Kotlin engine
// with a phone folder reached with java.io and a fake NAS.

private val t0 = LocalDateTime.of(2024, 5, 1, 10, 0).toInstant(ZoneOffset.UTC).toEpochMilli()
private const val MIN = 60_000L

class Harness(val local: File, var mode: String = SyncMode.BOTH, val fold: Boolean? = false) {
    val nas = FakeNas()
    val store = MemoryStore()
    val events = ArrayList<SyncEvent>()
    var exclude = emptyList<String>()
    var resumeMin = 8L shl 20
    var now = t0

    val pair get() = SyncPair("p1", "/srv/strata/main/Docs", "app:test", mode = mode, exclude = exclude)

    fun engine() = PairSync(nas.remote, store, SyncOptions("Laptop", now = { now }, zone = ZoneOffset.UTC, resumeMin = resumeMin), pair, FileFs(local, fold),
        onEvent = { synchronized(events) { events += it } })

    /** Rounds until nothing is left (at most 5), like the worker. */
    fun round(allow: Boolean = false, restore: Boolean = false): RoundResult {
        var r = engine().round(allow, restore)
        var i = 1
        while (r.error == null && r.again && i < 5) {
            r = engine().round()
            i++
        }
        return r
    }

    fun sync(allow: Boolean = false, restore: Boolean = false): RoundResult {
        val r = round(allow, restore)
        r.error?.let { throw AssertionError("sync: $it", it) }
        return r
    }

    fun write(rel: String, content: String, mt: Long) {
        val f = File(local, rel)
        f.parentFile!!.mkdirs()
        f.writeText(content)
        f.setLastModified(mt)
    }

    fun read(rel: String): String = File(local, rel).takeIf { it.isFile }?.readText() ?: "<missing>"

    /** The phone tree, without the trash. */
    fun tree(): String = local.walkTopDown().filter { it != local }.map { f ->
        f.relativeTo(local).invariantSeparatorsPath + if (f.isDirectory) "/" else ""
    }.filter { !it.startsWith("$TRASH_DIR/") }.sorted().joinToString(" ")

    /** What went to the trash: the trashed items, by path below their trash folder. */
    fun trashed(): List<String> {
        val t = File(local, TRASH_DIR)
        return t.listFiles().orEmpty().flatMap { stamp -> stamp.walkTopDown().filter { it != stamp }.map { it.relativeTo(stamp).invariantSeparatorsPath } }.sorted()
    }
}

class SyncEngineTest {
    @get:Rule val tmp = TemporaryFolder()

    private fun harness(mode: String = SyncMode.BOTH, fold: Boolean? = false) = Harness(tmp.newFolder(), mode, fold)

    @Test fun firstSyncBothWays() {
        val h = harness()
        h.write("local.txt", "from the phone", t0)
        h.write("sub/deep/l2.txt", "deep", t0)
        h.nas.put("remote.txt", "from the NAS", t0)
        h.nas.put("photos/2024/a.jpg", "jpeg", t0)
        h.write("same.txt", "identical", t0)
        h.nas.put("same.txt", "identical", t0 + 60 * MIN) // same content, other date: no conflict
        h.sync()
        val all = "local.txt photos/ photos/2024/ photos/2024/a.jpg remote.txt same.txt sub/ sub/deep/ sub/deep/l2.txt"
        assertEquals(all, h.tree())
        assertEquals(all, h.nas.list())
        assertEquals("jpeg", h.read("photos/2024/a.jpg"))
        assertEquals("deep", h.nas.get("sub/deep/l2.txt"))
        assertEquals(t0, File(h.local, "remote.txt").lastModified())
        // Nothing more to do.
        val up = h.nas.calls("upload")
        val down = h.nas.calls("download")
        h.sync()
        assertEquals(up, h.nas.calls("upload"))
        assertEquals(down, h.nas.calls("download"))
    }

    @Test fun changesEachSide() {
        val h = harness()
        h.write("a.txt", "v1", t0)
        h.nas.put("b.txt", "v1", t0)
        h.sync()
        h.write("a.txt", "v2 local", t0 + MIN)
        h.nas.put("b.txt", "v2 remote", t0 + MIN)
        h.sync()
        assertEquals("v2 local", h.nas.get("a.txt"))
        assertEquals("v2 remote", h.read("b.txt"))
        assertTrue(h.events.toString(), h.events.isEmpty())
    }

    @Test fun conflictKeepsBoth() {
        val h = harness()
        h.write("report.docx", "v1", t0)
        h.sync()
        h.write("report.docx", "local edit", t0 + MIN)
        h.nas.put("report.docx", "remote edit!", t0 + 2 * MIN)
        h.sync()
        val cname = "report (conflict from Laptop 2024-05-01 10-00).docx"
        assertEquals("remote edit!", h.read("report.docx"))
        assertEquals("local edit", h.read(cname))
        assertEquals("local edit", h.nas.get(cname))
        assertEquals("remote edit!", h.nas.get("report.docx"))
        assertEquals(listOf("conflict"), h.events.map { it.kind })
        // Both sides created the same name with different content.
        h.write("new.txt", "mine", t0)
        h.nas.put("new.txt", "theirs", t0)
        h.sync()
        assertEquals("theirs", h.read("new.txt"))
        assertEquals("mine", h.read("new (conflict from Laptop 2024-05-01 10-00).txt"))
    }

    // The NAS file changes while the phone one is sent: the copy sent is kept as the conflict copy, nothing is overwritten.
    @Test fun uploadConflict() {
        val h = harness()
        h.write("a.txt", "v1", t0)
        h.sync()
        h.write("a.txt", "local", t0 + MIN)
        h.nas.onUpload = { h.nas.put("a.txt", "remote", t0 + MIN) }
        h.sync()
        val cname = "a (conflict from Laptop 2024-05-01 10-00).txt"
        assertEquals("remote", h.read("a.txt"))
        assertEquals("local", h.read(cname))
        assertEquals("local", h.nas.get(cname))
        assertEquals("remote", h.nas.get("a.txt"))
    }

    @Test fun deletions() {
        val h = harness()
        h.write("keep.txt", "k", t0)
        h.write("gone-local.txt", "x", t0)
        h.nas.put("gone-remote.txt", "y", t0)
        h.nas.put("dir/inner.txt", "z", t0)
        h.sync()
        File(h.local, "gone-local.txt").delete()
        h.nas.remove("gone-remote.txt")
        h.nas.remove("dir")
        h.sync()
        assertEquals("keep.txt", h.nas.list())
        assertEquals(listOf("gone-local.txt"), h.nas.recycle)
        assertEquals("keep.txt", h.tree())
        // A folder goes to the trash whole.
        assertEquals(listOf("dir", "dir/inner.txt", "gone-remote.txt"), h.trashed())
        // Deleted here, changed there: the NAS version comes back.
        h.write("x.txt", "1", t0)
        h.sync()
        File(h.local, "x.txt").delete()
        h.nas.put("x.txt", "2", t0 + MIN)
        h.sync()
        assertEquals("2", h.read("x.txt"))
        // Changed here, deleted there: the phone version goes back.
        h.write("y.txt", "1", t0)
        h.sync()
        h.write("y.txt", "2", t0 + MIN)
        h.nas.remove("y.txt")
        h.sync()
        assertEquals("2", h.nas.get("y.txt"))
        // A folder deleted on the NAS while a new file was added inside it here.
        h.write("d2/old.txt", "o", t0)
        h.sync()
        h.write("d2/new.txt", "n", t0)
        h.nas.remove("d2")
        h.sync()
        h.sync()
        assertEquals("n", h.read("d2/new.txt"))
        assertEquals("n", h.nas.get("d2/new.txt"))
    }

    @Test fun trashKeptThirtyDays() {
        val h = harness()
        h.nas.put("old.txt", "o", t0)
        h.sync()
        h.nas.remove("old.txt")
        h.sync()
        assertEquals(listOf("old.txt"), h.trashed())
        h.now = t0 + 29L * 86_400_000
        h.sync()
        assertEquals(listOf("old.txt"), h.trashed())
        h.now = t0 + 31L * 86_400_000
        h.sync()
        assertEquals(emptyList<String>(), h.trashed())
        // The trash itself never goes to the NAS.
        assertTrue(h.nas.list().split(" ").none { it.startsWith(".trash") })
    }

    @Test fun movesWithoutTransfers() {
        val h = harness()
        h.write("a.txt", "aaa", t0)
        h.write("folder/b.txt", "bbb", t0)
        h.write("folder/sub/c.txt", "ccc", t0)
        h.sync()
        val up = h.nas.calls("upload")
        Files.move(File(h.local, "a.txt").toPath(), File(h.local, "renamed.txt").toPath())
        Files.move(File(h.local, "folder").toPath(), File(h.local, "moved").toPath())
        h.sync()
        assertEquals("moved/ moved/b.txt moved/sub/ moved/sub/c.txt renamed.txt", h.nas.list())
        assertEquals(up, h.nas.calls("upload"))
        assertEquals(2, h.nas.calls("move"))
        assertTrue(h.nas.recycle.isEmpty())
        // Moves on the NAS: renamed here, nothing downloaded.
        val down = h.nas.calls("download")
        h.nas.rename("renamed.txt", "back.txt")
        h.nas.rename("moved", "elsewhere")
        h.sync()
        assertEquals("back.txt elsewhere/ elsewhere/b.txt elsewhere/sub/ elsewhere/sub/c.txt", h.tree())
        assertEquals(down, h.nas.calls("download"))
        assertTrue(h.trashed().isEmpty())
        h.sync()
        assertEquals(up, h.nas.calls("upload"))
        assertEquals(down, h.nas.calls("download"))
    }

    @Test fun caseChange() {
        val h = harness(fold = true) // as on the shared storage of a phone
        h.write("readme.txt", "r", t0)
        h.sync()
        h.nas.rename("readme.txt", "README.txt")
        h.sync()
        assertEquals("README.txt", h.tree())
        Files.move(File(h.local, "README.txt").toPath(), File(h.local, "ReadMe.txt").toPath())
        h.sync()
        assertEquals("ReadMe.txt", h.nas.list())
        // Two NAS names the phone cannot hold side by side: one is skipped.
        h.nas.put("Data.csv", "1", t0)
        h.nas.put("data.csv", "2", t0)
        val r = h.sync()
        assertEquals(listOf("data.csv"), r.skipped.map { it.path })
        assertEquals("1", h.read("Data.csv"))
    }

    @Test fun interruptedTransfersResume() {
        val h = harness()
        val big = "0123456789".repeat(1000)
        h.nas.put("big.bin", big, t0)
        h.nas.failDownloadAt = 3000
        assertNotNull("no error for an interrupted download", h.round().error)
        assertEquals("<missing>", h.read("big.bin"))
        h.sync()
        assertEquals(big, h.read("big.bin"))
        // Upload: interrupted, then resumed with the same upload.
        h.resumeMin = 0
        h.write("up.bin", big + "x", t0)
        h.nas.failUploadAt = 4000
        assertNotNull("no error for an interrupted upload", h.round().error)
        h.sync()
        assertEquals(big + "x", h.nas.get("up.bin"))
        assertEquals(1, h.nas.calls("resumed"))
        // Changed on the NAS during the download: planned again, then right.
        h.nas.put("moving.txt", "v1", t0)
        h.nas.onDownload = { h.nas.put("moving.txt", "v2", t0 + MIN) }
        h.sync()
        assertEquals("v2", h.read("moving.txt"))
    }

    @Test fun deleteBrake() {
        for (apply in listOf(true, false)) {
            val h = harness()
            for (i in 0 until 30) h.nas.put("f$i.txt", "x", t0)
            h.sync()
            assertEquals(30, h.tree().split(" ").size)
            for (i in 0 until 25) h.nas.remove("f$i.txt")
            val r = h.sync()
            val b = r.brake
            assertNotNull("no brake", b)
            assertEquals(25, b!!.localDeletes)
            assertEquals(30, h.tree().split(" ").size)
            assertTrue(h.trashed().isEmpty())
            // The user answers.
            h.sync(allow = apply, restore = !apply)
            val after = h.sync()
            assertNull(after.brake)
            if (apply) assertEquals(5, h.tree().split(" ").size)
            else assertEquals(30, h.nas.list().split(" ").size)
        }
    }

    @Test fun ignoredExcludedAndLinks() {
        val h = harness()
        h.exclude = listOf("Big/Videos")
        h.write(".DS_Store", "junk", t0)
        h.write("~\$report.docx", "lock", t0)
        h.write("doc.tmp", "tmp", t0)
        h.write(".gitignore", "kept", t0)
        h.nas.put("Big/Videos/movie.mkv", "huge", t0)
        h.nas.put("Big/small.txt", "s", t0)
        Files.createSymbolicLink(File(h.local, "link").toPath(), File("/etc").toPath())
        val r = h.sync()
        assertEquals(".gitignore Big/ Big/Videos/ Big/Videos/movie.mkv Big/small.txt", h.nas.list())
        assertEquals("<missing>", h.read("Big/Videos/movie.mkv"))
        assertEquals("s", h.read("Big/small.txt"))
        assertEquals(1, r.links)
    }

    @Test fun invalidNamesSkipped() {
        val h = harness()
        h.nas.put("what?.txt", "q", t0)
        h.nas.put("a:b/c.txt", "c", t0)
        h.nas.put("fine.txt", "f", t0)
        val r = h.sync()
        assertEquals(listOf("a:b", "a:b/c.txt", "what?.txt"), r.skipped.map { it.path })
        assertEquals("fine.txt", h.tree())
    }

    @Test fun missingFoldersDeleteNothing() {
        val h = harness()
        h.write("a.txt", "a", t0)
        h.sync()
        val away = File(h.local.path + ".away")
        assertTrue(h.local.renameTo(away))
        assertTrue(h.round().error is LocalMissingException)
        assertEquals("a", h.nas.get("a.txt"))
        assertTrue(away.renameTo(h.local))
        // An unreadable phone folder is not a deleted one.
        h.write("locked/b.txt", "b", t0)
        h.sync()
        val locked = File(h.local, "locked")
        locked.setReadable(false)
        try {
            if (locked.list() == null) {
                h.sync()
                assertEquals("b", h.nas.get("locked/b.txt"))
            }
        } finally {
            locked.setReadable(true)
        }
    }

    @Test fun phoneToNasOnly() {
        val h = harness(SyncMode.UP)
        h.write("mine.txt", "m", t0)
        h.nas.put("theirs.txt", "t", t0)
        h.sync()
        assertEquals("mine.txt", h.tree()) // nothing comes down
        assertEquals("m", h.nas.get("mine.txt"))
        // Changed on both sides: the NAS keeps its version, the phone one goes up as a copy, the phone is not touched.
        h.write("mine.txt", "phone edit", t0 + MIN)
        h.nas.put("mine.txt", "nas edit", t0 + 2 * MIN)
        h.sync()
        assertEquals("phone edit", h.read("mine.txt"))
        assertEquals("nas edit", h.nas.get("mine.txt"))
        assertEquals("phone edit", h.nas.get("mine (conflict from Laptop 2024-05-01 10-00).txt"))
        assertEquals("mine.txt", h.tree())
        // Quiet afterwards, and a later change here goes up.
        val up = h.nas.calls("upload")
        h.sync()
        assertEquals(up, h.nas.calls("upload"))
        h.write("mine.txt", "again", t0 + 3 * MIN)
        h.sync()
        assertEquals("again", h.nas.get("mine.txt"))
        // Deleted on the NAS: not deleted here; deleted here: deleted there.
        h.nas.remove("mine.txt")
        h.sync()
        assertEquals("again", h.read("mine.txt"))
        h.write("gone.txt", "g", t0)
        h.sync()
        File(h.local, "gone.txt").delete()
        h.sync()
        assertNull(h.nas.get("gone.txt"))
    }

    @Test fun nasToPhoneOnly() {
        val h = harness(SyncMode.DOWN)
        h.write("mine.txt", "m", t0)
        h.nas.put("theirs.txt", "t", t0)
        h.sync()
        assertEquals("mine.txt theirs.txt", h.tree())
        assertEquals("theirs.txt", h.nas.list()) // nothing goes up
        // Changed on both sides: the NAS version wins here, the phone one stays as a copy on the phone.
        h.write("theirs.txt", "phone edit", t0 + MIN)
        h.nas.put("theirs.txt", "nas edit", t0 + 2 * MIN)
        h.sync()
        assertEquals("nas edit", h.read("theirs.txt"))
        assertEquals("phone edit", h.read("theirs (conflict from Laptop 2024-05-01 10-00).txt"))
        assertEquals("theirs.txt", h.nas.list())
        // Deleted here: not deleted on the NAS.
        File(h.local, "theirs.txt").delete()
        h.sync()
        assertEquals("nas edit", h.nas.get("theirs.txt"))
    }

    @Test fun names() {
        assertEquals("", localNameProblem("ok.txt"))
        assertTrue(localNameProblem("a:b").isNotEmpty())
        assertTrue(localNameProblem("what?.txt").isNotEmpty())
        assertEquals("", remoteNameProblem("what?.txt"))
        assertTrue(remoteNameProblem("a\\b").isNotEmpty())
        for ((name, want) in mapOf(
            "report.docx" to "report (conflict from Laptop 2024-05-01 10-00).docx",
            ".bashrc" to ".bashrc (conflict from Laptop 2024-05-01 10-00)",
            "archive" to "archive (conflict from Laptop 2024-05-01 10-00)",
        )) assertEquals(want, conflictName(name, "Laptop", t0, ZoneOffset.UTC, 1))
        assertEquals("a (conflict from My-PC--1- 2024-05-01 10-00 2).txt", conflictName("a.txt", "My/PC:\"1\"", t0, ZoneOffset.UTC, 2))
        val m = Matcher(Namer(true), DEFAULT_IGNORE, listOf("Photos/RAW"))
        for ((p, ign) in mapOf(
            "a/.DS_Store" to true, "THUMBS.DB" to true, "~\$x.docx" to true, "x.TMP" to true, ".~lock.a#" to true,
            "photos/raw/1.cr2" to true, "Photos/RAWS/1" to false, "Photos/raw" to true, "doc.txt" to false, ".strataos-sync-1.part" to true,
            ".trash" to true, ".trash/2024/x" to true, "a/.trash" to false,
        )) assertEquals(p, ign, m.ignored(p))
        assertTrue(globMatch("*.tmp", "a.tmp"))
        assertTrue(!globMatch("*.tmp", "a/b.tmp"))
        assertEquals(t0 - t0 % 1000, trashTime(trashFolder(t0, ZoneOffset.UTC).substringAfter('/'), ZoneOffset.UTC))
    }

    // makePlan on its own, for the cases the rounds above do not reach.
    @Test fun planTable() {
        fun f(p: String, size: Long, mt: Long) = LocalEntry(p, false, size, mt)
        fun rf(p: String, v: String, size: Long) = RemoteEntry(p, version = v, size = size)
        fun b(p: String, v: String, size: Long, mt: Long) = BaseEntry(p, version = v, size = size, localMtime = mt)
        val cases = listOf(
            Triple("unchanged", View(mapOf("a" to f("a", 1, 1)), mapOf("a" to rf("a", "v", 1)), mapOf("a" to b("a", "v", 1, 1))), ""),
            Triple("both deleted", View(emptyMap(), emptyMap(), mapOf("a" to b("a", "v", 1, 1))), "forget:a"),
            Triple("same size both new", View(mapOf("a" to f("a", 1, 1)), mapOf("a" to rf("a", "v", 1)), emptyMap()), "compare:a"),
            Triple("other size both new", View(mapOf("a" to f("a", 1, 1)), mapOf("a" to rf("a", "v", 2)), emptyMap()), "conflict:a"),
            Triple("file became folder here", View(mapOf("a" to LocalEntry("a", true)), mapOf("a" to rf("a", "v", 1)), mapOf("a" to b("a", "v", 1, 1))), "delete-remote:a"),
            Triple("unreadable folder", View(mapOf("d" to LocalEntry("d", true)), mapOf("d" to RemoteEntry("d", true), "d/x" to rf("d/x", "v", 1)),
                mapOf("d" to BaseEntry("d", dir = true), "d/x" to b("d/x", "v", 1, 1)), listOf("d")), ""),
            Triple("deletions deepest first", View(emptyMap(), mapOf("d" to RemoteEntry("d", true), "d/x" to rf("d/x", "v", 1)),
                mapOf("d" to BaseEntry("d", dir = true), "d/x" to b("d/x", "v", 1, 1))), "delete-remote:d/x delete-remote:d"),
        )
        for ((name, v, want) in cases) assertEquals(name, want, makePlan(v).ops.joinToString(" "))
        // One-way pairs drop what goes the other way.
        val v = View(mapOf("up" to f("up", 1, 1)), mapOf("down" to rf("down", "v", 1)), emptyMap())
        assertEquals("upload:up", filterForMode(makePlan(v), SyncMode.UP).ops.joinToString(" "))
        assertEquals("download:down", filterForMode(makePlan(v), SyncMode.DOWN).ops.joinToString(" "))
    }

    @Test fun manyFiles() {
        val h = harness()
        val n = 3000
        for (i in 0 until n) {
            val d = "d${i % 50}"
            if (i % 2 == 0) h.write("$d/l$i", "x", t0) else h.nas.put("$d/r$i", "y", t0)
        }
        h.sync()
        val start = System.nanoTime()
        val r = h.sync()
        val idle = (System.nanoTime() - start) / 1_000_000
        assertEquals(n, r.files)
        if (idle > 5000) fail("idle round too slow: $idle ms")
    }
}
