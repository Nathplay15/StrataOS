package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.backup.BackupOptions
import io.github.nathplay15.strataos.backup.BackupPlanner
import io.github.nathplay15.strataos.backup.MediaItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Test

class BackupPlannerTest {
    private fun item(id: Long, bucket: String = "cam", name: String = "IMG_$id.jpg", size: Long = 100, taken: Long = id * 1000, video: Boolean = false) =
        MediaItem(id, "content://media/$id", bucket, if (bucket == "cam") "Camera" else bucket, name, size, taken, taken + 5, video)

    private val opts = BackupOptions(setOf("cam", "shots"), videos = true, destination = "/srv/strata/main/Photos/Pixel/")

    @Test fun pendingSkipsDoneOtherAlbumsAndVideosWhenAsked() {
        val items = listOf(item(1), item(2), item(3, bucket = "whatsapp"), item(4, video = true), item(5, size = 0), item(6, name = ".trashed-IMG.jpg"))
        val done = setOf(items[0].key)
        assertEquals(listOf(4L, 2L), BackupPlanner.pending(items, done, opts).map { it.id })
        assertEquals(listOf(2L), BackupPlanner.pending(items, done, opts.copy(videos = false)).map { it.id })
    }

    @Test fun editedPhotoIsSentAgain() {
        val a = item(1)
        val edited = a.copy(size = 120, dateModified = a.dateModified + 60_000)
        assertNotEquals(a.key, edited.key)
        assertEquals(listOf(1L), BackupPlanner.pending(listOf(edited), setOf(a.key), opts).map { it.id })
    }

    @Test fun keepsTheDateTaken() {
        assertEquals(3000L, item(3).originalDate)
        assertEquals(42L, item(3).copy(dateTaken = 0, dateModified = 42).originalDate)
    }

    @Test fun foldersPerAlbum() {
        assertEquals("/srv/strata/main/Photos/Pixel/Camera", BackupPlanner.folderFor(opts.destination, item(1)))
        assertEquals("a_b", BackupPlanner.safeName("a/b"))
        assertEquals("hidden", BackupPlanner.safeName("..hidden"))
        assertEquals("Phone", BackupPlanner.safeName(" .. "))
        assertEquals("x_y", BackupPlanner.safeName("x\ny"))
        assertFalse(BackupPlanner.validName("a/b.jpg"))
    }

    @Test fun batchesByFolder() {
        val items = (1L..7L).map { item(it, bucket = if (it % 2 == 0L) "cam" else "shots") }
        val b = BackupPlanner.batches(items, "/d", max = 2)
        assertEquals(listOf("/d/shots", "/d/shots", "/d/Camera", "/d/Camera"), b.map { it.first })
        assertEquals(7, b.sumOf { it.second.size })
        assertEquals(listOf(1L, 3L), b[0].second.map { it.id })
    }
}
