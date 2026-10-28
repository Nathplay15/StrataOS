package io.github.nathplay15.strataos.net

import kotlinx.coroutines.delay
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

/** A background job of the NAS (volume creation, pulls, updates…). */
@Serializable data class Job(
    val id: String = "", val type: String = "", val title: String = "", val user: String = "", val status: String = "",
    val progress: Double = 0.0, val error: String = "", val started: String = "", val finished: String? = null, val log: String = "",
) {
    val running get() = status == "running"
    val failed get() = status == "failed"
}

fun Api.job(id: String): Job = call(get("/api/jobs/$id"), Job.serializer())

/** Polls a job until it ends; [onUpdate] sees every state. */
suspend fun Api.awaitJob(start: Job, onUpdate: (Job) -> Unit = {}): Job {
    var j = start
    onUpdate(j)
    while (j.running) {
        delay(1000)
        j = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { job(start.id) }
        onUpdate(j)
    }
    return j
}

/** The job an API call answered with, if it is one (most calls answer {"ok":true} or a job). */
fun JsonObject.asJob(): Job? {
    val id = (this["id"] as? JsonPrimitive)?.content ?: return null
    val status = (this["status"] as? JsonPrimitive)?.content ?: return null
    return Job(id = id, status = status, title = (this["title"] as? JsonPrimitive)?.content ?: "")
}
