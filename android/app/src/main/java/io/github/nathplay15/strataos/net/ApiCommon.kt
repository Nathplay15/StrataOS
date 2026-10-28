package io.github.nathplay15.strataos.net

import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import okhttp3.HttpUrl
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

// Helpers shared by the management areas (net/Api<Area>.kt).

/** A background job of the NAS (VM creation, updates, installs…). */
@Serializable data class NasJob(
    val id: String = "", val type: String = "", val title: String = "", val status: String = "",
    val progress: Double = 0.0, val error: String = "", val log: String = "",
) {
    val running get() = status == "running"
}

val okSer = JsonObject.serializer()

fun <T> listSer(s: KSerializer<T>) = ListSerializer(s)

/** Sends [body] (already JSON) with [method]. */
fun Api.send(method: String, path: String, body: String? = null, vararg q: Pair<String, String>): Request {
    val rb = (body ?: if (method == "GET" || method == "DELETE") null else "{}")?.toRequestBody(jsonType)
    return request(url(path, *q)).method(method, rb).build()
}

inline fun <reified B> Api.sendJson(method: String, path: String, v: B, vararg q: Pair<String, String>): Request =
    send(method, path, json.encodeToString(kotlinx.serialization.serializer<B>(), v), *q)

fun Api.ok(method: String, path: String, vararg q: Pair<String, String>): JsonObject = call(send(method, path, null, *q), okSer)

fun Api.job(id: String): NasJob = call(get("/api/jobs/$id"), NasJob.serializer())

/** Starts a job (the answer of the NAS is its snapshot). */
fun Api.startJob(method: String, path: String, body: String? = null): NasJob = call(send(method, path, body), NasJob.serializer())

/** The headers that authenticate this app (for players and image loaders that build their own requests). */
fun Api.authHeaders(): Map<String, String> {
    val h = request(base).build().headers
    return h.names().associateWith { h[it] ?: "" }
}

/** Raw bytes of a GET (pictures). */
fun Api.bytes(url: HttpUrl): ByteArray = http.newCall(request(url).get().build()).execute().use { r ->
    if (!r.isSuccessful) throw Api.apiError(r.code, r.body.string())
    r.body.bytes()
}

/** Raw bytes of a POST with a JSON body (camera test picture). */
fun Api.postBytes(path: String, body: String): ByteArray =
    http.newCall(request(url(path)).post(body.toRequestBody(jsonType)).build()).execute().use { r ->
        if (!r.isSuccessful) throw Api.apiError(r.code, r.body.string())
        r.body.bytes()
    }

fun JsonElement.obj(): JsonObject = this as JsonObject
