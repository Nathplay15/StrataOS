package io.github.nathplay15.strataos.net.manage

import io.github.nathplay15.strataos.net.Api
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put

// The pages of the web Control Panel shown natively by the app (ui/control/):
// notification centre, backups, system upgrade, energy and UPS, hardware,
// name and time, logs, scheduled tasks, routines, security advisor, address
// blocking, sessions, login journal, antivirus, certificates. Same routes
// and rights as the web interface. Objects the app sends back (switching a
// task on or off) are the ones the NAS gave, changed in one field, like the
// web interface does ({...task, enabled}): nothing the app does not know of
// is lost.

// ---- JSON reading ----

fun JsonElement?.o(): JsonObject = this as? JsonObject ?: JsonObject(emptyMap())
fun JsonElement?.a(): List<JsonElement> = (this as? JsonArray) ?: emptyList()
fun JsonObject.s(k: String): String = (this[k] as? JsonPrimitive)?.takeIf { it !is JsonNull }?.content ?: ""
fun JsonObject.b(k: String): Boolean = (this[k] as? JsonPrimitive)?.booleanOrNull ?: false
fun JsonObject.l(k: String): Long = (this[k] as? JsonPrimitive)?.longOrNull ?: (this[k] as? JsonPrimitive)?.doubleOrNull?.toLong() ?: 0
fun JsonObject.d(k: String): Double? = (this[k] as? JsonPrimitive)?.doubleOrNull
fun JsonObject.objs(k: String): List<JsonObject> = this[k].a().map { it.o() }
fun JsonObject.with(k: String, v: Boolean): JsonObject = JsonObject(this + (k to JsonPrimitive(v)))

fun Api.json(path: String, vararg q: Pair<String, String>): JsonElement = call(get(path, *q), JsonElement.serializer())
fun Api.jsonList(path: String, vararg q: Pair<String, String>): List<JsonObject> = json(path, *q).a().map { it.o() }
fun Api.sendObj(method: String, path: String, body: JsonElement? = null): JsonElement = call(send(method, path, body?.toString()), JsonElement.serializer())

// ---- Notification centre (api/notif_center.go) ----

fun Api.notifications(): JsonObject = json("/api/notifications").o()
fun Api.readNotifications(upTo: Long = 0) = sendObj("POST", "/api/notifications/read", buildJsonObject { put("upTo", upTo) })
fun Api.clearNotifications() = sendObj("POST", "/api/notifications/clear")

// ---- Backups (h_backups.go) ----

fun Api.backups(): List<JsonObject> = jsonList("/api/backups")
fun Api.runBackup(id: String) = sendObj("POST", "/api/backups/${enc(id)}/run")
fun Api.resyncBackup(id: String) = sendObj("POST", "/api/backups/${enc(id)}/resync")
fun Api.saveBackup(b: JsonObject) = sendObj("PUT", "/api/backups/${enc(b.s("id"))}", b)
fun Api.deleteBackup(id: String) = sendObj("DELETE", "/api/backups/${enc(id)}")
fun Api.backupSnapshots(id: String): List<JsonObject> = jsonList("/api/backups/${enc(id)}/snapshots")

// ---- System upgrade (Debian, h_distupgrade.go) ----

fun Api.distUpgrade(): JsonObject = json("/api/system/dist-upgrade").o()
fun Api.distCheck(): NasJob = startJob("POST", "/api/system/dist-upgrade/check")
fun Api.distDismiss() = sendObj("POST", "/api/system/dist-upgrade/dismiss")
fun Api.distShow() = sendObj("POST", "/api/system/dist-upgrade/show")
fun Api.distUnplan() = sendObj("DELETE", "/api/system/dist-upgrade/scheduled")
fun Api.distReboot() = sendObj("POST", "/api/system/dist-upgrade/reboot")

/** Starts the upgrade now or plans it for the maintenance window: a job, or the new state. */
fun Api.distStart(now: Boolean, force: Boolean, confirm: String): JsonObject =
    sendObj("POST", "/api/system/dist-upgrade/start", buildJsonObject { put("when", if (now) "now" else "window"); put("force", force); put("confirm", confirm) }).o()

// ---- Energy, UPS, hardware ----

fun Api.energy(): JsonObject = json("/api/energy").o()
fun Api.energyPower(): JsonObject = json("/api/energy/power").o()
fun Api.standby(): JsonElement = sendObj("POST", "/api/energy/standby")
fun Api.wake(mac: String) = sendObj("POST", "/api/energy/wake", buildJsonObject { put("mac", mac) })
fun Api.ups(): JsonObject = json("/api/ups").o()
fun Api.hardware(): JsonObject = json("/api/hardware").o()

// ---- Name and time ----

fun Api.setHostname(h: String) = sendObj("PUT", "/api/system/hostname", buildJsonObject { put("hostname", h) })
fun Api.timezones(): List<String> = json("/api/system/timezones").a().map { (it as JsonPrimitive).content }
fun Api.setTimezone(zone: String) = sendObj("PUT", "/api/system/time", buildJsonObject { put("timezone", zone); put("ntp", true) })
fun Api.currentSettings(): JsonObject = json("/api/settings").o()

// ---- Logs, scheduled tasks, routines ----

fun Api.journal(unit: String, priority: String, grep: String): List<JsonObject> =
    jsonList("/api/system/logs", "unit" to unit, "priority" to priority, "grep" to grep, "lines" to "500")
fun Api.nasEvents(): List<JsonObject> = jsonList("/api/system/events", "limit" to "500")
fun Api.schedules(): List<JsonObject> = jsonList("/api/schedules")
fun Api.saveSchedule(s: JsonObject) = sendObj("PUT", "/api/schedules/${enc(s.s("id"))}", s)
fun Api.runSchedule(id: String) = sendObj("POST", "/api/schedules/${enc(id)}/run")
fun Api.deleteSchedule(id: String) = sendObj("DELETE", "/api/schedules/${enc(id)}")
fun Api.routines(): List<JsonObject> = jsonList("/api/routines")
fun Api.saveRoutine(r: JsonObject) = sendObj("PUT", "/api/routines/${enc(r.s("id"))}", r)
fun Api.fireRoutine(id: String) = sendObj("POST", "/api/routines/${enc(id)}/fire")
fun Api.deleteRoutine(id: String) = sendObj("DELETE", "/api/routines/${enc(id)}")

// ---- Security ----

fun Api.advisor(): JsonObject = json("/api/security/advisor").o()
fun Api.runAdvisor(): JsonObject = sendObj("POST", "/api/security/advisor/run").o()
fun Api.fixAdvice(id: String): JsonObject = sendObj("POST", "/api/security/advisor/fix/${enc(id)}").o()
fun Api.ignoreAdvice(id: String, ignored: Boolean): JsonObject =
    sendObj("PUT", "/api/security/advisor/ignore", buildJsonObject { put("id", id); put("ignored", ignored) }).o()
fun Api.guard(): JsonObject = json("/api/guard").o()
fun Api.unblock(ip: String) = sendObj("POST", "/api/guard/unblock", buildJsonObject { put("ip", ip) })
fun Api.block(ip: String) = sendObj("POST", "/api/guard/block", buildJsonObject { put("ip", ip); put("minutes", 60 * 24 * 365) })
fun Api.sessions(): List<JsonObject> = jsonList("/api/sessions")
fun Api.endSession(id: String) = sendObj("DELETE", "/api/sessions/${enc(id)}")
fun Api.endOtherSessions(): JsonObject = sendObj("DELETE", "/api/sessions/others").o()
fun Api.logins(): List<JsonObject> = jsonList("/api/logins")
fun Api.antivirus(): JsonObject = json("/api/antivirus").o()
fun Api.restoreQuarantined(id: String): JsonObject = sendObj("POST", "/api/antivirus/quarantine/${enc(id)}/restore").o()
fun Api.deleteQuarantined(id: String) = sendObj("DELETE", "/api/antivirus/quarantine/${enc(id)}")
fun Api.certificates(): JsonObject = json("/api/system/certificates").o()

private fun enc(s: String): String = java.net.URLEncoder.encode(s, "UTF-8").replace("+", "%20")
