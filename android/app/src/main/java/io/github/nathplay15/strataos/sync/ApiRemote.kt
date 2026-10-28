package io.github.nathplay15.strataos.sync

import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.json
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.util.concurrent.TimeUnit
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.Response
import okio.BufferedSink

// The Sync API of the NAS (docs/DEVELOPMENT.md, "Sync API"; backend
// api/sync*.go): the same calls as desktop/internal/api, with the phone's
// device token. Any device token may use it, with the rights of its user.

/** An entry of the Sync API (mtime in Unix milliseconds). */
@Serializable
data class SyncEntry(
    val path: String = "", val dir: Boolean = false, val size: Long = 0, val mtime: Long = 0, val version: String = "",
    val hash: String = "", val id: String = "", val deleted: Boolean = false,
) {
    fun toRemote(rel: String = path) = RemoteEntry(rel, dir, size, mtime, version, hash, id)
}

@Serializable private data class ChangesPage(val cursor: String = "", val reset: Boolean = false, val more: Boolean = false, val changes: List<SyncEntry> = emptyList())
@Serializable private data class HashesReq(val folder: String, val paths: List<String>)
@Serializable private data class HashesRes(val hashes: Map<String, String> = emptyMap())
@Serializable private data class PathReq(val folder: String, val path: String, val ifMatch: String? = null)
@Serializable private data class MoveReq(val folder: String, val from: String, val to: String, val ifMatch: String)
@Serializable private data class UploadCreate(
    val folder: String, val name: String, val size: Long, val modTime: Long, val sync: Boolean = true, val replace: Boolean = true,
    val ifMatch: String, val mkdirs: Boolean = true,
)
@Serializable private data class UploadRes(val id: String = "", val offset: Long = 0, val size: Long = 0, val done: Boolean = false, val entry: SyncEntry? = null)
@Serializable private data class CommitReq(val name: String, val ifMatch: String)

/** A shared folder the user may follow. */
@Serializable
data class SyncShare(val name: String = "", val path: String = "", val writable: Boolean = false, val recycleBin: Boolean = false)

fun Api.syncShares(): List<SyncShare> = call(get("/api/sync/shares"), ListSerializer(SyncShare.serializer()))

/** The NAS through the Sync API. */
class ApiRemote(private val api: Api) : Remote {
    // Long polls answer within 55 s.
    private val longPoll = api.http.newBuilder().readTimeout(90, TimeUnit.SECONDS).build()

    private fun <T> run(req: Request, rel: String, read: (Response) -> T, http: okhttp3.OkHttpClient = api.http): T =
        http.newCall(req).execute().use { r ->
            if (!r.isSuccessful) throw errorOf(r, rel)
            read(r)
        }

    private inline fun <reified B> post(path: String, body: B): Request =
        api.request(api.url(path)).post(api.body(body)).build()

    override fun changes(folder: String, cursor: String, waitSec: Int): Changes {
        val q = arrayOf("folder" to folder, "cursor" to cursor, "limit" to "2000") + (if (waitSec > 0) arrayOf("wait" to waitSec.toString()) else emptyArray())
        val p = run(api.get("/api/sync/changes", *q), "", { json.decodeFromString(ChangesPage.serializer(), it.body.string()) }, if (waitSec > 0) longPoll else api.http)
        return Changes(p.cursor, p.reset, p.more, p.changes.filter { !it.deleted }.map { it.toRemote() }, p.changes.filter { it.deleted }.map { it.path })
    }

    override fun hashes(folder: String, paths: List<String>): Map<String, String> =
        run(post("/api/sync/hashes", HashesReq(folder, paths)), "", { json.decodeFromString(HashesRes.serializer(), it.body.string()).hashes })

    override fun download(folder: String, rel: String, version: String, offset: Long, out: OutputStream): Long {
        val b = api.request(api.url("/api/sync/file", "folder" to folder, "path" to rel)).get()
        if (offset > 0) {
            b.header("Range", "bytes=$offset-")
            if (version.isNotEmpty()) b.header("If-Range", "\"$version\"")
        }
        return run(b.build(), rel, { r ->
            val got = r.header("X-Strata-Version") ?: ""
            if (version.isNotEmpty() && got != version) throw ChangedException()
            if (offset > 0 && r.code != 206) throw ChangedException() // the range was refused: start again
            r.body.byteStream().use { it.copyTo(out, 64 * 1024) }
        })
    }

    override fun upload(folder: String, u: UploadReq): RemoteEntry {
        val dir = parentOf(u.rel)
        val dest = if (dir.isEmpty()) folder else "$folder/$dir"
        var id = ""
        var off = 0L
        if (u.resumeId.isNotEmpty()) {
            runCatching { run(api.get("/api/mobile/uploads/${u.resumeId}"), u.rel, { json.decodeFromString(UploadRes.serializer(), it.body.string()) }) }
                .getOrNull()?.takeIf { it.size == u.size }?.let { id = u.resumeId; off = it.offset }
        }
        if (id.isEmpty()) {
            val v = run(post("/api/mobile/uploads", UploadCreate(dest, u.rel.substringAfterLast('/'), u.size, u.mtime, ifMatch = u.ifMatch)), u.rel,
                { json.decodeFromString(UploadRes.serializer(), it.body.string()) })
            if (v.done) return (v.entry ?: SyncEntry()).toRemote(u.rel)
            id = v.id
            off = v.offset
            u.onId(id)
        }
        while (true) {
            u.progress(off)
            val n = minOf(CHUNK, u.size - off)
            val start = off
            val body = object : RequestBody() {
                override fun contentType() = "application/offset+octet-stream".toMediaType()
                override fun contentLength() = n
                override fun writeTo(sink: BufferedSink) {
                    u.open(start).use { input -> copyN(input, sink, n) { u.progress(start + it) } }
                }
            }
            val req = api.request(api.url("/api/mobile/uploads/$id")).patch(body).header("Upload-Offset", off.toString()).build()
            val res = api.http.newCall(req).execute().use { r ->
                if (r.code == 409 && r.header("Upload-Offset") != null) {
                    // Out of step (a chunk partly written before): go on from there.
                    off = r.header("Upload-Offset")!!.toLong()
                    null
                } else if (!r.isSuccessful) {
                    val e = errorOf(r, u.rel)
                    if (e is ConflictException) e.uploadId = id
                    throw e
                } else json.decodeFromString(UploadRes.serializer(), r.body.string())
            } ?: continue
            if (res.done) return (res.entry ?: SyncEntry()).toRemote(u.rel)
            if (res.offset <= off) throw IOException("upload not progressing")
            off = res.offset
        }
    }

    override fun commit(folder: String, uploadId: String, rel: String, ifMatch: String): RemoteEntry =
        run(post("/api/mobile/uploads/$uploadId/commit", CommitReq(rel.substringAfterLast('/'), ifMatch)), rel, { r ->
            val o = json.decodeFromString(JsonObject.serializer(), r.body.string())
            (o["entry"]?.let { json.decodeFromJsonElement(SyncEntry.serializer(), it) } ?: SyncEntry()).toRemote(rel)
        })

    override fun mkdir(folder: String, rel: String): RemoteEntry =
        run(post("/api/sync/mkdir", PathReq(folder, rel)), rel, { json.decodeFromString(SyncEntry.serializer(), it.body.string()).toRemote(rel) })

    override fun move(folder: String, from: String, to: String, ifMatch: String): RemoteEntry =
        run(post("/api/sync/move", MoveReq(folder, from, to, ifMatch)), from, { json.decodeFromString(SyncEntry.serializer(), it.body.string()).toRemote(to) })

    override fun delete(folder: String, rel: String, ifMatch: String) {
        run(post("/api/sync/delete", PathReq(folder, rel, ifMatch)), rel, { })
    }

    companion object {
        const val CHUNK = 8L shl 20

        /** Turns an answer of the NAS into the engine's errors. */
        fun errorOf(r: Response, rel: String): IOException {
            val text = runCatching { r.body.string() }.getOrDefault("")
            val o = runCatching { json.decodeFromString(JsonObject.serializer(), text) }.getOrNull()
            val msg = o?.get("error")?.jsonPrimitive?.contentOrNull ?: "HTTP ${r.code}"
            val conflict = o?.get("conflict")?.jsonPrimitive?.booleanOrNull == true
            return when {
                r.code == 412 || conflict -> {
                    val cur = o?.get("current")?.takeIf { it is JsonObject }?.let { json.decodeFromJsonElement(SyncEntry.serializer(), it).toRemote(rel) }
                    ConflictException(msg, cur)
                }
                r.code == 401 -> UnauthorizedException()
                r.code == 404 -> NotFoundException(msg)
                else -> ApiException(r.code, msg)
            }
        }

        private fun copyN(input: InputStream, sink: BufferedSink, n: Long, progress: (Long) -> Unit) {
            val buf = ByteArray(64 * 1024)
            var left = n
            while (left > 0) {
                val k = input.read(buf, 0, minOf(buf.size.toLong(), left).toInt())
                if (k < 0) throw IOException("file shorter than expected")
                sink.write(buf, 0, k)
                left -= k
                progress(n - left)
            }
        }
    }
}
