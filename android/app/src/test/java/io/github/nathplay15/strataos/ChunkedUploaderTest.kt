package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.CreateUpload
import io.github.nathplay15.strataos.net.UploadState
import io.github.nathplay15.strataos.upload.ChunkSource
import io.github.nathplay15.strataos.upload.ChunkedUploader
import io.github.nathplay15.strataos.upload.ResumeStore
import io.github.nathplay15.strataos.upload.UploadTransport
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** A NAS in memory that keeps what it received, like the real one, even when a chunk breaks off. */
class FakeNas : UploadTransport {
    val files = HashMap<String, ByteArrayOutputStream>()
    val sizes = HashMap<String, Long>()
    var created = 0
    var dropAfter: Int? = null // next patch breaks after this many bytes
    var forgetNext = false
    var serverError = 0
    var finished: ByteArray? = null

    override fun create(req: CreateUpload): UploadState {
        val id = "u${++created}"
        files[id] = ByteArrayOutputStream()
        sizes[id] = req.size
        return UploadState(id, 0, req.size)
    }

    override fun status(id: String): UploadState {
        val f = files[id] ?: throw ApiException(410, "gone")
        return UploadState(id, f.size().toLong(), sizes[id]!!)
    }

    override fun patch(id: String, offset: Long, length: Long, source: InputStream, progress: (Long) -> Unit): UploadState {
        if (forgetNext) { forgetNext = false; files.remove(id); throw ApiException(410, "gone") }
        if (serverError > 0) { serverError--; throw ApiException(503, "busy") }
        val f = files[id] ?: throw ApiException(410, "gone")
        if (offset != f.size().toLong()) throw ApiException(409, "offset mismatch", offset = f.size().toLong())
        val limit = dropAfter
        dropAfter = null
        val data = source.readNBytes(length.toInt())
        if (limit != null) {
            f.write(data, 0, limit)
            throw IOException("connection reset")
        }
        f.write(data)
        progress(length)
        val size = sizes[id]!!
        if (f.size().toLong() == size) {
            finished = f.toByteArray()
            files.remove(id)
            return UploadState(id, size, size, done = true, path = "/srv/strata/main/x")
        }
        return UploadState(id, f.size().toLong(), size)
    }

    override fun cancel(id: String) { files.remove(id) }
}

class MapResume : ResumeStore {
    val m = HashMap<String, String>()
    override fun get(key: String) = m[key]
    override fun put(key: String, uploadId: String) { m[key] = uploadId }
    override fun remove(key: String) { m.remove(key) }
}

class BytesSource(private val b: ByteArray) : ChunkSource {
    var opens = 0
    override val size = b.size.toLong()
    override fun openAt(offset: Long): InputStream { opens++; return ByteArrayInputStream(b, offset.toInt(), b.size - offset.toInt()) }
}

class ChunkedUploaderTest {
    private val data = ByteArray(1000) { (it * 7).toByte() }
    private val req = CreateUpload("/srv/strata/main/Photos", "IMG_1.jpg", data.size.toLong())

    @Test fun sendsInChunks() {
        val nas = FakeNas()
        val resume = MapResume()
        val progress = ArrayList<Long>()
        val st = ChunkedUploader(nas, resume, chunkSize = 300, sleep = {}).upload("k", req, BytesSource(data)) { s, _ -> progress += s }
        assertTrue(st.done)
        assertArrayEquals(data, nas.finished)
        assertEquals(1000L, progress.last())
        assertNull(resume.get("k"))
    }

    @Test fun resumesAfterDroppedConnection() {
        val nas = FakeNas()
        val src = BytesSource(data)
        nas.dropAfter = 120 // the first chunk breaks off after 120 bytes: kept by the NAS
        var slept = 0
        val st = ChunkedUploader(nas, MapResume(), chunkSize = 300, sleep = { slept++ }).upload("k", req, src)
        assertTrue(st.done)
        assertArrayEquals(data, nas.finished)
        assertEquals(1, nas.created)
        assertEquals(1, slept)
    }

    @Test fun resumesInALaterRun() {
        val nas = FakeNas()
        val resume = MapResume()
        nas.dropAfter = 50
        val failing = ChunkedUploader(nas, resume, chunkSize = 300, retries = 0, sleep = {})
        try { failing.upload("k", req, BytesSource(data)); fail("no error") } catch (_: IOException) { }
        assertEquals("u1", resume.get("k")) // the app was killed here; next run:
        val st = ChunkedUploader(nas, resume, chunkSize = 300, sleep = {}).upload("k", req, BytesSource(data))
        assertTrue(st.done)
        assertEquals(1, nas.created)
        assertArrayEquals(data, nas.finished)
    }

    @Test fun startsOverWhenTheNasForgot() {
        val nas = FakeNas()
        val resume = MapResume()
        resume.put("k", "u-unknown")
        nas.forgetNext = false
        val up = ChunkedUploader(nas, resume, chunkSize = 400, sleep = {})
        val st = up.upload("k", req, BytesSource(data))
        assertTrue(st.done)
        assertArrayEquals(data, nas.finished)
        // Expired in the middle of an upload.
        val nas2 = FakeNas()
        nas2.forgetNext = true
        assertTrue(ChunkedUploader(nas2, MapResume(), chunkSize = 400, sleep = {}).upload("k", req, BytesSource(data)).done)
        assertEquals(2, nas2.created)
        assertArrayEquals(data, nas2.finished)
    }

    @Test fun followsTheServerOffset() {
        val nas = FakeNas()
        val resume = MapResume()
        val first = nas.create(req)
        nas.files[first.id]!!.write(data, 0, 250) // the NAS has more than the app thinks
        resume.put("k", first.id)
        val st = ChunkedUploader(nas, resume, chunkSize = 300, sleep = {}).upload("k", req, BytesSource(data))
        assertTrue(st.done)
        assertArrayEquals(data, nas.finished)
    }

    @Test fun retriesServerErrorsThenGivesUp() {
        val nas = FakeNas()
        nas.serverError = 2
        assertTrue(ChunkedUploader(nas, MapResume(), chunkSize = 300, sleep = {}).upload("k", req, BytesSource(data)).done)
        val nas2 = FakeNas()
        nas2.serverError = 10
        try {
            ChunkedUploader(nas2, MapResume(), chunkSize = 300, retries = 3, sleep = {}).upload("k", req, BytesSource(data))
            fail("no error")
        } catch (e: ApiException) {
            assertEquals(503, e.status)
        }
    }

    @Test fun emptyFile() {
        val nas = object : UploadTransport by FakeNas() {
            override fun create(req: CreateUpload) = UploadState("e", 0, 0, done = true)
        }
        assertTrue(ChunkedUploader(nas, MapResume(), sleep = {}).upload("k", req.copy(size = 0), BytesSource(ByteArray(0))).done)
    }
}
