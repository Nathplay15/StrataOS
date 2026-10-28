package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.data.Favorite
import io.github.nathplay15.strataos.data.SyncAction
import io.github.nathplay15.strataos.data.syncAction
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.ui.files.FileOrder
import io.github.nathplay15.strataos.ui.files.SortBy
import io.github.nathplay15.strataos.ui.files.displayPath
import io.github.nathplay15.strataos.ui.files.naturalCompare
import io.github.nathplay15.strataos.ui.files.recycleOrigin
import io.github.nathplay15.strataos.ui.files.sortEntries
import io.github.nathplay15.strataos.ui.sampleSize
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class FilesV2Test {
    private fun f(name: String, dir: Boolean = false, size: Long = 0, time: String = "2026-01-01T00:00:00Z") = FileEntry(name, "/srv/strata/main/$name", dir, size, time)

    private val list = listOf(
        f("photo10.jpg", size = 30, time = "2026-03-01T10:00:00Z"),
        f("Docs", dir = true, time = "2025-01-01T00:00:00Z"),
        f("photo2.jpg", size = 10, time = "2026-05-01T10:00:00+02:00"),
        f("archive.zip", size = 500, time = "2024-01-01T00:00:00Z"),
        f("alpha", dir = true, time = "2026-06-01T00:00:00Z"),
    )

    @Test fun foldersFirstNaturalNames() {
        assertEquals(listOf("alpha", "Docs", "archive.zip", "photo2.jpg", "photo10.jpg"), sortEntries(list, FileOrder()).map { it.name })
        assertEquals(listOf("Docs", "alpha", "photo10.jpg", "photo2.jpg", "archive.zip"), sortEntries(list, FileOrder(SortBy.NAME, true)).map { it.name })
    }

    @Test fun byDateAndSize() {
        assertEquals(listOf("alpha", "Docs", "photo2.jpg", "photo10.jpg", "archive.zip"), sortEntries(list, FileOrder(SortBy.DATE, true)).map { it.name })
        assertEquals(listOf("alpha", "Docs", "photo2.jpg", "photo10.jpg", "archive.zip"), sortEntries(list, FileOrder(SortBy.SIZE)).map { it.name })
        assertEquals(listOf("alpha", "Docs", "photo2.jpg", "photo10.jpg", "archive.zip"), sortEntries(list, FileOrder(SortBy.TYPE)).map { it.name })
    }

    @Test fun natural() {
        assertTrue(naturalCompare("a2", "a10") < 0)
        assertTrue(naturalCompare("A", "b") < 0)
        assertTrue(naturalCompare("abc", "abcd") < 0)
    }

    @Test fun recycleBin() {
        assertEquals("/srv/strata/main/Docs/a/b.txt", recycleOrigin("/srv/strata/main/Docs/.recycle/alice/a/b.txt"))
        assertNull(recycleOrigin("/srv/strata/main/Docs/.recycle/alice"))
        assertNull(recycleOrigin("/srv/strata/main/Docs/a.txt"))
        assertEquals("/main/Docs", displayPath("/srv/strata/main/Docs"))
        assertEquals("/", displayPath("/srv/strata"))
    }

    @Test fun offlineSync() {
        val fav = Favorite("/srv/strata/a.pdf", "a.pdf", offline = true, size = 10, modTime = "t1")
        assertEquals(SyncAction.KEEP, syncAction(fav, 10, "t1", true))
        assertEquals(SyncAction.DOWNLOAD, syncAction(fav, 11, "t1", true))
        assertEquals(SyncAction.DOWNLOAD, syncAction(fav, 10, "t2", true))
        assertEquals(SyncAction.DOWNLOAD, syncAction(fav, 10, "t1", false))
        assertEquals(SyncAction.MISSING, syncAction(fav, null, null, true))
    }

    @Test fun thumbnailSampling() {
        assertEquals(1, sampleSize(300, 200, 256))
        assertEquals(4, sampleSize(4000, 3000, 900))
        assertEquals(8, sampleSize(4096, 4096, 512))
    }
}
