package io.github.nathplay15.strataos.net

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okio.BufferedSink
import okio.source

@Serializable data class Me(val username: String = "", val fullName: String = "", val role: String = "user", val twoFactor: Boolean = false)
@Serializable data class ServerMeta(val hostname: String = "", val version: String = "")
@Serializable data class Device(val id: String = "", val name: String = "", val model: String = "", val push: Boolean = false, val pushError: String = "")
@Serializable data class PairResponse(val token: String, val device: Device = Device(), val user: Me = Me(), val server: ServerMeta = ServerMeta())
@Serializable data class MobileInfo(val hostname: String = "", val version: String = "", val vapidKey: String = "", val user: Me = Me(), val device: Device? = null)
@Serializable data class FileEntry(val name: String, val path: String, val dir: Boolean = false, val size: Long = 0, val modTime: String = "", val link: Boolean = false)
@Serializable data class FileList(val path: String = "", val entries: List<FileEntry> = emptyList(), val writable: Boolean = false)
@Serializable data class UploadState(
    val id: String = "", val offset: Long = 0, val size: Long = 0, val name: String = "", val folder: String = "",
    val done: Boolean = false, val duplicate: Boolean = false, val path: String = "",
)
@Serializable data class CreateUpload(val folder: String, val name: String, val size: Long, val modTime: Long = 0, val sha256: String = "", val mkdirs: Boolean = false)
@Serializable data class ExistsFile(val name: String, val size: Long, val modTime: Long = 0, val sha256: String = "")
@Serializable data class ExistsRequest(val folder: String, val files: List<ExistsFile>)
@Serializable data class ExistsResult(val name: String = "", val exists: Boolean = false, val `as`: String = "")
@Serializable data class ExistsResponse(val files: List<ExistsResult> = emptyList())
@Serializable data class NasEvent(val id: Long = 0, val time: String = "", val level: String = "info", val subject: String = "", val message: String = "")
@Serializable data class Events(val cursor: Long = 0, val events: List<NasEvent> = emptyList())
@Serializable data class Alert(val level: String = "", val message: String = "")
@Serializable data class Volume(val name: String = "", val kind: String = "", val mountPoint: String = "", val total: Long = 0, val used: Long = 0, val free: Long = 0, val health: String = "")
@Serializable data class Overview(val disks: Int = 0, val volumes: List<Volume> = emptyList(), val alerts: List<Alert> = emptyList(), val shares: Int = 0)
@Serializable data class SystemInfo(val hostname: String = "", val version: String = "", val uptime: Double = 0.0, val cpuModel: String = "", val memTotal: Long = 0, val load: List<Double> = emptyList())
@Serializable data class Quota(val name: String = "", val used: Long = 0, val limit: Long = 0)
@Serializable private data class PairPassword(val username: String, val password: String, val code: String = "", val deviceName: String, val model: String, val app: String)
@Serializable private data class PairCode(val pairingCode: String, val deviceName: String, val model: String, val app: String)
@Serializable private data class PushRegistration(val pushUrl: String, val p256dh: String = "", val auth: String = "", val app: String = "")
@Serializable private data class PathBody(val path: String)

/** An answer of the NAS other than 2xx. [twoFactor]: a two-factor code is required or was wrong. */
class ApiException(val status: Int, message: String, val twoFactor: Boolean = false, val offset: Long? = null) : IOException(message)

val json = Json { ignoreUnknownKeys = true; explicitNulls = false; encodeDefaults = true }

/** Builds the HTTP client for a NAS: certificate pinning, timeouts suited to large uploads. */
fun nasHttpClient(pin: String?, caOnly: Boolean): OkHttpClient {
    val tm = PinningTrustManager(pin, caOnly)
    val ctx = SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), null) }
    return OkHttpClient.Builder()
        .sslSocketFactory(ctx.socketFactory, tm)
        .hostnameVerifier(tm.hostnameVerifier())
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(120, TimeUnit.SECONDS)
        .writeTimeout(120, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()
}

/** Calls the StrataOS API with a device token. */
class Api(baseUrl: String, val http: OkHttpClient, private val token: String?) {
    val base: HttpUrl = baseUrl.trimEnd('/').toHttpUrl()
    val jsonType = "application/json".toMediaType()

    fun url(path: String, vararg query: Pair<String, String>): HttpUrl {
        val b = base.newBuilder().encodedPath(path)
        query.forEach { (k, v) -> b.addQueryParameter(k, v) }
        return b.build()
    }

    fun request(url: HttpUrl): Request.Builder = Request.Builder().url(url).apply {
        if (token != null) header("Authorization", "Bearer $token")
        header("X-Strata", "android")
    }

    // call, parse, body and get are public for the API functions of each area
    // (extension functions in net/Api<Area>.kt).
    fun <T> call(req: Request, ser: KSerializer<T>): T = http.newCall(req).execute().use { r -> parse(r, ser) }

    fun <T> parse(r: Response, ser: KSerializer<T>): T {
        val body = r.body.string()
        if (!r.isSuccessful) throw apiError(r.code, body)
        return json.decodeFromString(ser, body.ifEmpty { "{}" })
    }

    inline fun <reified B> body(v: B): RequestBody = json.encodeToString(kotlinx.serialization.serializer<B>(), v).toRequestBody(jsonType)

    fun get(path: String, vararg q: Pair<String, String>) = request(url(path, *q)).get().build()

    fun pairWithPassword(username: String, password: String, code: String, deviceName: String, model: String, app: String): PairResponse =
        call(request(url("/api/mobile/pair")).post(body(PairPassword(username, password, code, deviceName, model, app))).build(), PairResponse.serializer())

    fun pairWithCode(pairingCode: String, deviceName: String, model: String, app: String): PairResponse =
        call(request(url("/api/mobile/pair")).post(body(PairCode(pairingCode, deviceName, model, app))).build(), PairResponse.serializer())

    fun info(): MobileInfo = call(get("/api/mobile/info"), MobileInfo.serializer())
    fun files(path: String): FileList = call(get("/api/files", "path" to path), FileList.serializer())
    fun mkdir(path: String) = call(request(url("/api/files/mkdir")).post(body(PathBody(path))).build(), JsonObject.serializer())
    fun overview(): Overview = call(get("/api/system/overview"), Overview.serializer())
    fun systemInfo(): SystemInfo = call(get("/api/system/info"), SystemInfo.serializer())
    fun quotas(): List<Quota> = call(get("/api/me/quotas"), kotlinx.serialization.builtins.ListSerializer(Quota.serializer()))
    fun events(since: Long): Events = call(get("/api/mobile/events", "since" to since.toString()), Events.serializer())

    fun registerPush(endpoint: String, p256dh: String, auth: String, app: String) =
        call(request(url("/api/mobile/device")).put(body(PushRegistration(endpoint, p256dh, auth, app))).build(), JsonObject.serializer())

    /** Forgets this phone on the NAS (its token stops working). */
    fun signOut() = call(request(url("/api/mobile/device")).delete().build(), JsonObject.serializer())

    fun createUpload(c: CreateUpload): UploadState = call(request(url("/api/mobile/uploads")).post(body(c)).build(), UploadState.serializer())
    fun uploadStatus(id: String): UploadState = call(get("/api/mobile/uploads/$id"), UploadState.serializer())
    fun cancelUpload(id: String) { runCatching { call(request(url("/api/mobile/uploads/$id")).delete().build(), JsonObject.serializer()) } }

    /** Sends [length] bytes from [source] at [offset]; a 409 answer carries the server's offset. */
    fun patchUpload(id: String, offset: Long, length: Long, source: InputStream, progress: (Long) -> Unit): UploadState {
        val chunk = object : RequestBody() {
            override fun contentType() = "application/offset+octet-stream".toMediaType()
            override fun contentLength() = length
            override fun writeTo(sink: BufferedSink) {
                val buf = ByteArray(64 * 1024)
                var left = length
                while (left > 0) {
                    val n = source.read(buf, 0, minOf(buf.size.toLong(), left).toInt())
                    if (n < 0) throw IOException("file shorter than expected")
                    sink.write(buf, 0, n)
                    left -= n
                    progress(length - left)
                }
            }
        }
        val req = request(url("/api/mobile/uploads/$id")).patch(chunk).header("Upload-Offset", offset.toString()).build()
        return call(req, UploadState.serializer())
    }

    fun exists(req: ExistsRequest): ExistsResponse = call(request(url("/api/mobile/exists")).post(body(req)).build(), ExistsResponse.serializer())

    /** Downloads a file to [dest] (written to a temporary file first). */
    fun download(path: String, dest: File, progress: (Long, Long) -> Unit = { _, _ -> }) {
        http.newCall(get("/api/files/download", "path" to path).newBuilder().build()).execute().use { r ->
            if (!r.isSuccessful) throw apiError(r.code, r.body.string())
            val total = r.body.contentLength()
            val tmp = File(dest.parentFile, dest.name + ".part")
            tmp.outputStream().use { out ->
                r.body.byteStream().use { input ->
                    val buf = ByteArray(64 * 1024)
                    var done = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        done += n
                        progress(done, total)
                    }
                }
            }
            if (!tmp.renameTo(dest)) throw IOException("cannot write ${dest.name}")
        }
    }

    /** Uploads a small file in one request (folders of the file browser). */
    fun uploadSmall(folder: String, name: String, file: File) {
        val part = object : RequestBody() {
            override fun contentType() = "application/octet-stream".toMediaType()
            override fun contentLength() = file.length()
            override fun writeTo(sink: BufferedSink) { file.source().use { sink.writeAll(it) } }
        }
        val mp = okhttp3.MultipartBody.Builder().setType(okhttp3.MultipartBody.FORM).addFormDataPart("file", name, part).build()
        http.newCall(request(url("/api/files/upload", "path" to folder)).post(mp).build()).execute().use { r ->
            if (!r.isSuccessful) throw apiError(r.code, r.body.string())
        }
    }

    companion object {
        fun apiError(code: Int, body: String): ApiException {
            val o = runCatching { json.decodeFromString(JsonObject.serializer(), body) }.getOrNull()
            val msg = o?.get("error")?.jsonPrimitive?.contentOrNull ?: "HTTP $code"
            val tfa = o?.get("twoFactor")?.jsonPrimitive?.booleanOrNull ?: false
            val off = o?.get("offset")?.jsonPrimitive?.contentOrNull?.toLongOrNull()
            return ApiException(code, msg, tfa, off)
        }
    }
}
