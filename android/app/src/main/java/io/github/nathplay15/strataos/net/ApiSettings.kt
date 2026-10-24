package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject

// NAS settings shown natively (updates, notifications, network, services,
// restore points, power) and the web interface session of the app.

// ---- Updates (api/h_system.go, h_updates.go) ----

@Serializable data class StrataUpdate(val running: String = "", val installed: String = "", val candidate: String = "", val repository: Boolean = false)
@Serializable data class Upgradable(val `package`: String = "", val current: String = "", val new: String = "", val source: String = "")

@Serializable data class AutoUpdateRun(val status: String = "", val started: String = "", val message: String = "", val dryRun: Boolean = false)
@Serializable data class AutoUpdates(
    val strata: String = "off", val security: String = "off", val system: String = "off", val days: List<Int> = emptyList(),
    val hour: Int = 3, val reboot: Boolean = false, val channel: String = "stable", val installed: String = "", val beta: Boolean = false,
    val nextWindow: String = "", val last: AutoUpdateRun? = null, val repository: Boolean = false,
)
@Serializable data class AutoUpdatesReq(val strata: String, val security: String, val system: String, val days: List<Int>, val hour: Int, val reboot: Boolean)
@Serializable private data class ChannelReq(val channel: String)

fun Api.strataUpdate(): StrataUpdate = call(get("/api/system/updates/strata"), StrataUpdate.serializer())
fun Api.upgradable(): List<Upgradable> = call(get("/api/system/updates"), ListSerializer(Upgradable.serializer()))
fun Api.checkUpdates(): NasJob = startJob("POST", "/api/system/updates/refresh")
fun Api.installStrataUpdate(): NasJob = startJob("POST", "/api/system/updates/strata")
fun Api.installSystemUpdates(): NasJob = startJob("POST", "/api/system/updates/upgrade")
fun Api.autoUpdates(): AutoUpdates = call(get("/api/system/updates/auto"), AutoUpdates.serializer())
fun Api.saveAutoUpdates(r: AutoUpdatesReq) = call(sendJson("PUT", "/api/system/updates/auto", r), okSer)

/** Switches the channel; the NAS then checks for updates (a job, or nothing when one runs already). */
fun Api.setUpdateChannel(channel: String): NasJob? {
    val o = call(sendJson("PUT", "/api/system/updates/channel", ChannelReq(channel)), JsonObject.serializer())
    return if (o.containsKey("id")) json.decodeFromJsonElement(NasJob.serializer(), o) else null
}

// ---- Notifications (store.Settings, round-tripped whole) ----

@Serializable data class NotifyChannel(val disabled: Boolean = false, val minLevel: String = "")
@Serializable data class NotifyChannels(
    val email: NotifyChannel = NotifyChannel(), val telegram: NotifyChannel = NotifyChannel(), val ntfy: NotifyChannel = NotifyChannel(),
    val webhook: NotifyChannel = NotifyChannel(), val mobile: NotifyChannel = NotifyChannel(),
)
@Serializable data class QuietHours(val enabled: Boolean = false, val start: String = "", val end: String = "", val silenceAll: Boolean = false)
@Serializable data class NotifySettings(
    val notifyEmail: String = "", val smtpHost: String = "", val telegramToken: String = "", val telegramChatId: String = "",
    val ntfyUrl: String = "", val webhookUrl: String = "", val notifyChannels: NotifyChannels = NotifyChannels(),
    val quietHours: QuietHours = QuietHours(), val monthlyReport: Boolean = false,
)
@Serializable data class TestNotification(val sent: List<String> = emptyList())

fun Api.notifySettings(): NotifySettings = call(get("/api/settings"), NotifySettings.serializer())

/**
 * Saves the notification preferences. PUT /api/settings replaces every
 * setting: the current ones are sent back as they came (secrets masked, which
 * the NAS keeps) with only these fields replaced.
 */
fun Api.saveNotifySettings(channels: NotifyChannels, quiet: QuietHours, monthlyReport: Boolean) {
    val cur = call(get("/api/settings"), JsonObject.serializer())
    val body = mergeJson(cur, mapOf(
        "notifyChannels" to json.encodeToJsonElement(NotifyChannels.serializer(), channels),
        "quietHours" to json.encodeToJsonElement(QuietHours.serializer(), quiet),
        "monthlyReport" to kotlinx.serialization.json.JsonPrimitive(monthlyReport),
    ))
    call(send("PUT", "/api/settings", body.toString()), okSer)
}

fun Api.testNotification(): TestNotification = call(send("POST", "/api/settings/test-notification", "{\"level\":\"\"}"), TestNotification.serializer())

/** [base] with the [changes] keys replaced (other keys kept as they are). */
fun mergeJson(base: JsonObject, changes: Map<String, JsonElement>): JsonObject = buildJsonObject {
    base.forEach { (k, v) -> if (k !in changes) put(k, v) }
    changes.forEach { (k, v) -> put(k, v) }
}

// ---- Network (read only) ----

@Serializable data class NetIface(val name: String = "", val mac: String = "", val state: String = "", val mtu: Int = 0, val addresses: List<String> = emptyList(), val speed: Int = 0, val kind: String = "")
@Serializable data class NetStatus(val interfaces: List<NetIface> = emptyList(), val gateway: String = "", val dns: List<String> = emptyList(), val hostname: String = "")
@Serializable data class NetworkInfo(val status: NetStatus = NetStatus(), val manager: String = "")

fun Api.network(): NetworkInfo = call(get("/api/system/network"), NetworkInfo.serializer())

// ---- Services ----

@Serializable data class NasService(val unit: String = "", val name: String = "", val active: String = "", val sub: String = "", val enabled: String = "", val loaded: Boolean = false) {
    val isActive get() = active == "active"
    val isEnabled get() = enabled == "enabled"
}
@Serializable data class FileServicesConfig(val webdav: Boolean = false, val ftp: Boolean = false, val ftpRequireTls: Boolean = false, val ftpPassiveMin: Int = 0, val ftpPassiveMax: Int = 0, val sftp: Boolean = false)
@Serializable data class FileServices(val config: FileServicesConfig = FileServicesConfig(), val ftpInstalled: Boolean = false, val sftpInstalled: Boolean = false)

fun Api.services(): List<NasService> = call(get("/api/system/services"), ListSerializer(NasService.serializer()))
/** start, stop, restart, enable (and start), disable (and stop). */
fun Api.serviceAction(unit: String, action: String) = ok("POST", "/api/system/services/$unit/$action")
fun Api.fileServices(): FileServices = call(get("/api/fileservices"), FileServices.serializer())
fun Api.saveFileServices(c: FileServicesConfig) = call(sendJson("PUT", "/api/fileservices", c), okSer)

// ---- Restore points ----

@Serializable data class RestorePoint(val name: String = "", val created: String = "", val label: String = "", val info: String = "", val invalid: Boolean = false)
@Serializable data class RestorePoints(val supported: Boolean = false, val reason: String = "", val root: String = "", val mode: String = "", val points: List<RestorePoint> = emptyList())

fun Api.restorePoints(): RestorePoints = call(get("/api/system/restore-points"), RestorePoints.serializer())
fun Api.createRestorePoint() = ok("POST", "/api/system/restore-points")
fun Api.deleteRestorePoint(name: String) = ok("DELETE", "/api/system/restore-points/$name")
fun Api.rollbackRestorePoint(name: String) = ok("POST", "/api/system/restore-points/$name/rollback")

// ---- Power ----

/** reboot or poweroff. */
fun Api.power(action: String) = ok("POST", "/api/system/power/$action")

// ---- Web interface (api/mobile_websession.go) ----

@Serializable data class WebSessionCode(val code: String = "", val path: String = "")

fun Api.webSession(): WebSessionCode = call(send("POST", "/api/mobile/websession"), WebSessionCode.serializer())
