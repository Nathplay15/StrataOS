package io.github.nathplay15.strataos.upload

import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.CreateUpload
import io.github.nathplay15.strataos.net.UploadState
import java.io.IOException
import java.io.InputStream

/** The server side of a resumable upload (the NAS API, or a fake in tests). */
interface UploadTransport {
    fun create(req: CreateUpload): UploadState
    /** Current state; throws [ApiException] 410 when the upload is gone (expired, cancelled). */
    fun status(id: String): UploadState
    fun patch(id: String, offset: Long, length: Long, source: InputStream, progress: (Long) -> Unit): UploadState
    fun cancel(id: String)
}

class ApiTransport(private val api: Api) : UploadTransport {
    override fun create(req: CreateUpload) = api.createUpload(req)
    override fun status(id: String) = api.uploadStatus(id)
    override fun patch(id: String, offset: Long, length: Long, source: InputStream, progress: (Long) -> Unit) = api.patchUpload(id, offset, length, source, progress)
    override fun cancel(id: String) = api.cancelUpload(id)
}

/** Where the upload ID of a file is kept between attempts (survives app restarts). */
interface ResumeStore {
    fun get(key: String): String?
    fun put(key: String, uploadId: String)
    fun remove(key: String)
}

/** The bytes to send, readable from any offset (a content URI is reopened and skipped). */
interface ChunkSource {
    val size: Long
    fun openAt(offset: Long): InputStream
}

/**
 * Sends a file in chunks the NAS appends at the announced offset. After a
 * network drop the NAS keeps what it received: the uploader asks for the
 * offset and goes on from there, now or in a later run (the upload ID is in
 * [resume]). An upload the NAS forgot (two days idle, NAS restored) starts over.
 */
class ChunkedUploader(
    private val transport: UploadTransport,
    private val resume: ResumeStore,
    private val chunkSize: Long = 8L shl 20,
    private val retries: Int = 5,
    private val sleep: (Long) -> Unit = { Thread.sleep(it) },
) {
    fun upload(key: String, req: CreateUpload, source: ChunkSource, progress: (sent: Long, total: Long) -> Unit = { _, _ -> }): UploadState {
        var state = start(key, req)
        var failures = 0
        while (!state.done) {
            val offset = state.offset
            val len = minOf(chunkSize, state.size - offset)
            try {
                state = source.openAt(offset).use { input ->
                    transport.patch(state.id, offset, len, input) { n -> progress(offset + n, state.size) }
                }
                failures = 0
                progress(state.offset, state.size)
            } catch (e: ApiException) {
                when {
                    e.status == 409 && e.offset != null -> state = state.copy(offset = e.offset)
                    e.status == 410 -> { resume.remove(key); state = start(key, req) }
                    e.status >= 500 && ++failures <= retries -> { sleep(backoff(failures)); state = refresh(key, req, state) }
                    else -> { if (e.status in 400..499 && e.status != 401 && e.status != 507) resume.remove(key); throw e }
                }
            } catch (e: IOException) {
                // Dropped connection: whatever reached the NAS is kept, ask where to go on.
                if (++failures > retries) throw e
                sleep(backoff(failures))
                state = refresh(key, req, state)
            }
        }
        resume.remove(key)
        return state
    }

    private fun backoff(n: Int): Long = minOf(30_000L, 1000L shl (n - 1))

    private fun start(key: String, req: CreateUpload): UploadState {
        resume.get(key)?.let { id ->
            try {
                val s = transport.status(id)
                if (s.size == req.size) return s
                transport.cancel(id)
            } catch (e: ApiException) {
                if (e.status != 410 && e.status != 404) throw e
            }
            resume.remove(key)
        }
        val s = transport.create(req)
        if (!s.done) resume.put(key, s.id)
        return s
    }

    private fun refresh(key: String, req: CreateUpload, cur: UploadState): UploadState = try {
        transport.status(cur.id)
    } catch (e: ApiException) {
        if (e.status != 410) throw e
        resume.remove(key)
        start(key, req)
    }
}
