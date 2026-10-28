package io.github.nathplay15.strataos.net.manage

import io.github.nathplay15.strataos.net.*

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer

// Multi-NAS console (backend api/h_fleet.go, h_fleet_updates.go). Administrators only.

@Serializable data class FleetSample(val cpu: Double = 0.0, val memUsed: Long = 0, val memTotal: Long = 0)
@Serializable data class FleetBackups(val ok: Int = 0, val failed: Int = 0, val lastFailure: String = "")
@Serializable data class FleetUpdateInfo(
    val version: String = "", val installed: String = "", val candidate: String = "", val system: Int = 0,
    val rebootRequired: Boolean = false, val aptBusy: Boolean = false, val checked: String = "",
)
@Serializable data class FleetAccess(val updates: Boolean = false, val reboot: Boolean = false)
@Serializable data class FleetHa(val role: String = "", val peers: List<String> = emptyList())
@Serializable data class FleetContainers(val running: Int = 0, val total: Int = 0)

@Serializable data class FleetSummary(
    val hostname: String = "", val version: String = "", val uptimeDays: Double = 0.0, val disks: Int = 0,
    val volumes: List<Volume> = emptyList(), val alerts: List<Alert> = emptyList(), val shares: Int = 0, val users: Int = 0,
    val containers: FleetContainers = FleetContainers(), val sample: FleetSample? = null, val backups: FleetBackups = FleetBackups(),
    val updates: FleetUpdateInfo = FleetUpdateInfo(), val access: FleetAccess? = null, val ha: FleetHa? = null,
)

@Serializable data class FleetNas(
    val id: String = "", val name: String = "", val host: String = "", val online: Boolean = false, val error: String = "",
    val summary: FleetSummary? = null, val haWith: List<String> = emptyList(),
) {
    val local get() = id == "local"
    val errors get() = summary?.alerts?.count { it.level == "error" || it.level == "danger" } ?: 0
}

@Serializable data class FleetItem(
    val member: String = "", val name: String = "", val host: String = "", val ha: String = "", val state: String = "",
    val progress: Double = 0.0, val from: String = "", val target: String = "", val version: String = "",
    val rebooted: Boolean = false, val note: String = "", val error: String = "",
)
@Serializable data class FleetTotals(val success: Int = 0, val failed: Int = 0, val skipped: Int = 0, val active: Int = 0, val pending: Int = 0, val progress: Double = 0.0)
@Serializable data class FleetRun(
    val id: String = "", val kind: String = "", val strategy: String = "", val reboot: Boolean = false, val dryRun: Boolean = false,
    val cancel: Boolean = false, val user: String = "", val status: String = "", val started: String = "", val finished: String? = null,
    val items: List<FleetItem> = emptyList(), val totals: FleetTotals = FleetTotals(),
) {
    val running get() = status == "running"
}

@Serializable data class FleetManager(
    val id: String = "", val name: String = "", val created: String = "", val lastSeen: String = "",
    val allowUpdates: Boolean = false, val allowReboot: Boolean = false,
)

@Serializable data class FleetUpdateReq(val ids: List<String>, val kind: String, val strategy: String, val reboot: Boolean, val dryRun: Boolean)
@Serializable private data class FleetIds(val ids: List<String>)
@Serializable private data class FleetAdd(val host: String, val code: String)
@Serializable private data class FleetAccessReq(val allowUpdates: Boolean, val allowReboot: Boolean)
@Serializable data class FleetCode(val code: String = "")

fun Api.fleet(): List<FleetNas> = call(get("/api/fleet"), ListSerializer(FleetNas.serializer()))
fun Api.addFleetNas(host: String, code: String) = call(sendJson("POST", "/api/fleet", FleetAdd(host, code)), okSer)
fun Api.removeFleetNas(id: String) = ok("DELETE", "/api/fleet/$id")
fun Api.fleetCheck(ids: List<String>): NasJob = call(sendJson("POST", "/api/fleet/check", FleetIds(ids)), NasJob.serializer())
fun Api.fleetRuns(): List<FleetRun> = call(get("/api/fleet/updates"), ListSerializer(FleetRun.serializer()))
fun Api.startFleetUpdate(r: FleetUpdateReq): FleetRun = call(sendJson("POST", "/api/fleet/updates", r), FleetRun.serializer())
fun Api.cancelFleetRun(id: String) = ok("POST", "/api/fleet/updates/$id/cancel")

/** The consoles that may watch this NAS, and the code that lets a new one in. */
fun Api.fleetManagers(): List<FleetManager> = call(get("/api/fleet/managers"), ListSerializer(FleetManager.serializer()))
fun Api.setFleetManager(id: String, updates: Boolean, reboot: Boolean) = call(sendJson("PUT", "/api/fleet/managers/$id", FleetAccessReq(updates, reboot)), okSer)
fun Api.removeFleetManager(id: String) = ok("DELETE", "/api/fleet/managers/$id")
fun Api.fleetCode(): FleetCode = call(send("POST", "/api/fleet/code"), FleetCode.serializer())
