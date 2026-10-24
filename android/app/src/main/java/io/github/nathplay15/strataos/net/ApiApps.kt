package io.github.nathplay15.strataos.net

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.MapSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.JsonObject
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.WebSocket
import okhttp3.WebSocketListener

// Applications (api/h_docker.go, h_docker_updates.go, h_uninstall.go), administrators only.

@Serializable data class DockerInfo(val available: Boolean = false, val error: String = "", val compose: Boolean = false)
@Serializable data class PortBinding(@SerialName("PrivatePort") val privatePort: Int = 0, @SerialName("PublicPort") val publicPort: Int = 0, @SerialName("Type") val type: String = "")
@Serializable data class Container(
    @SerialName("Id") val id: String = "", @SerialName("Names") val names: List<String> = emptyList(), @SerialName("Image") val image: String = "",
    @SerialName("State") val state: String = "", @SerialName("Status") val status: String = "", @SerialName("Labels") val labels: Map<String, String>? = null,
    @SerialName("Ports") val ports: List<PortBinding>? = null, @SerialName("Created") val created: Long = 0,
) {
    val name get() = names.firstOrNull()?.removePrefix("/") ?: id.take(12)
    val stack get() = labels?.get("com.docker.compose.project") ?: ""
    val running get() = state == "running"
}
@Serializable data class ContainerStats(
    val cpuPercent: Double = 0.0, val memUsage: Long = 0, val memLimit: Long = 0, val netRx: Long = 0, val netTx: Long = 0,
    val blockRead: Long = 0, val blockWrite: Long = 0, val pids: Long = 0,
)
@Serializable data class DockerImage(
    @SerialName("Id") val id: String = "", @SerialName("RepoTags") val repoTags: List<String>? = null, @SerialName("Size") val size: Long = 0,
    @SerialName("Created") val created: Long = 0, @SerialName("Containers") val containers: Int = -1,
) {
    val title get() = repoTags?.firstOrNull { it != "<none>:<none>" } ?: id.removePrefix("sha256:").take(12)
}
@Serializable data class Stack(
    val name: String = "", val status: String = "", val services: Int = 0, val running: Int = 0, val app: String = "", val modifiedAt: String = "",
)
@Serializable data class AppVar(val key: String = "", val label: String = "", val default: String = "", val type: String = "text", val from: String = "", val length: Int = 0)
@Serializable data class CatalogApp(
    val id: String = "", val name: String = "", val category: String = "", val description: String = "", val icon: String = "",
    val webPort: String = "", val vars: List<AppVar> = emptyList(),
)
@Serializable data class CustomApp(val stack: String = "", val name: String = "", val description: String = "", val webPort: Int = 0, val status: String = "")
@Serializable data class AppCatalog(
    val apps: List<CatalogApp> = emptyList(), val installed: Map<String, String> = emptyMap(), val ports: Map<String, String> = emptyMap(),
    val linked: Map<String, Map<String, String>> = emptyMap(), val custom: List<CustomApp> = emptyList(),
)
@Serializable data class AppPlan(
    val stack: String = "", val app: String = "", val name: String = "", val containers: Int = 0, val volumes: List<String>? = null,
    val folders: List<String>? = null, val images: List<String>? = null, val integrations: List<String>? = null,
)
@Serializable data class ContainerUpdate(
    val id: String = "", val name: String = "", val image: String = "", val stack: String = "", val status: String = "", val error: String = "",
)
@Serializable data class DockerUpdates(
    val checked: String = "", val running: Boolean = false, val containers: List<ContainerUpdate> = emptyList(),
    val stacks: Map<String, Boolean> = emptyMap(), val available: Int = 0,
) {
    fun container(id: String) = containers.firstOrNull { it.id == id }
    fun hasUpdate(c: Container) = container(c.id)?.status == "available"
    fun stackHasUpdate(stack: String) = stacks[stack] == true
}

@Serializable private data class Install(val name: String, val values: Map<String, String>)
@Serializable private data class Uninstall(val deleteData: Boolean, val removeImages: Boolean)
@Serializable private data class Pull(val image: String)
@Serializable private data class Apply(val container: String = "", val stack: String = "")

private fun Api.postJson(path: String, b: okhttp3.RequestBody = "{}".toRequestBody(jsonType)): JsonObject =
    call(request(url(path)).post(b).build(), JsonObject.serializer())

fun Api.dockerInfo(): DockerInfo = call(get("/api/docker/info"), DockerInfo.serializer())
fun Api.containers(): List<Container> = call(get("/api/docker/containers"), ListSerializer(Container.serializer()))
fun Api.containerStats(): Map<String, ContainerStats> = call(get("/api/docker/stats"), MapSerializer(String.serializer(), ContainerStats.serializer()))
fun Api.containerAction(id: String, action: String) { postJson("/api/docker/containers/$id/$action") }
fun Api.deleteContainer(id: String, volumes: Boolean) {
    call(request(if (volumes) url("/api/docker/containers/$id", "volumes" to "1") else url("/api/docker/containers/$id")).delete().build(), JsonObject.serializer())
}
fun Api.images(): List<DockerImage> = call(get("/api/docker/images"), ListSerializer(DockerImage.serializer()))
fun Api.pullImage(image: String): Job = call(request(url("/api/docker/images/pull")).post(body(Pull(image))).build(), Job.serializer())
fun Api.deleteImage(id: String) { call(request(url("/api/docker/images/$id")).delete().build(), JsonObject.serializer()) }
fun Api.stacks(): List<Stack> = call(get("/api/docker/stacks"), ListSerializer(Stack.serializer()))
fun Api.stackAction(name: String, action: String): Job = call(request(url("/api/docker/stacks/$name/$action")).post("{}".toRequestBody(jsonType)).build(), Job.serializer())
fun Api.stackLogs(name: String): String = call(get("/api/docker/stacks/$name/logs", "tail" to "300"), MapSerializer(String.serializer(), String.serializer()))["logs"] ?: ""
fun Api.deleteStack(name: String, volumes: Boolean) {
    call(request(if (volumes) url("/api/docker/stacks/$name", "volumes" to "1") else url("/api/docker/stacks/$name")).delete().build(), JsonObject.serializer())
}
fun Api.appCatalog(): AppCatalog = call(get("/api/apps"), AppCatalog.serializer())
fun Api.installApp(id: String, name: String, values: Map<String, String>): Job =
    call(request(url("/api/apps/$id/install")).post(body(Install(name, values))).build(), Job.serializer())
fun Api.uninstallPlan(stack: String): AppPlan = call(get("/api/apps/$stack/uninstall"), AppPlan.serializer())
fun Api.uninstallApp(stack: String, deleteData: Boolean, removeImages: Boolean): Job =
    call(request(url("/api/apps/$stack/uninstall")).post(body(Uninstall(deleteData, removeImages))).build(), Job.serializer())

fun Api.dockerUpdates(): DockerUpdates = call(get("/api/docker/updates"), DockerUpdates.serializer())
fun Api.checkUpdates(): Job = call(request(url("/api/docker/updates/check")).post("{}".toRequestBody(jsonType)).build(), Job.serializer())
fun Api.updateContainer(id: String): Job = call(request(url("/api/docker/updates/apply")).post(body(Apply(container = id))).build(), Job.serializer())
fun Api.updateStack(name: String): Job = call(request(url("/api/docker/updates/apply")).post(body(Apply(stack = name))).build(), Job.serializer())
fun Api.updateAll(): Job = call(request(url("/api/docker/updates/apply-all")).post("{}".toRequestBody(jsonType)).build(), Job.serializer())

/** Follows the logs of a container (WebSocket of the NAS, JSON lines {"s": stream, "l": line}). */
fun Api.followLogs(id: String, listener: WebSocketListener): WebSocket {
    val u = url("/api/docker/containers/$id/logs", "tail" to "300").newBuilder().scheme(if (base.isHttps) "https" else "http").build()
    return http.newWebSocket(request(u).build(), listener)
}
