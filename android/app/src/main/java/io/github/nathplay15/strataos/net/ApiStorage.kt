package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.RequestBody.Companion.toRequestBody

// Storage manager (api/h_storage.go, h_shares.go, h_quota.go), administrators only.

@Serializable data class Disk(
    val name: String = "", val path: String = "", val type: String = "", val size: Long = 0, val model: String = "", val serial: String = "",
    val vendor: String = "", val rotational: Boolean = false, val transport: String = "", val fsType: String = "", val label: String = "",
    val mountpoints: List<String?>? = null, val removable: Boolean = false, val kind: String = "", val system: Boolean = false,
    val usage: String = "", val usedBy: String = "", val temperature: Int = 0, val healthy: Boolean? = null, val powerOnHours: Int = 0, val standby: Boolean = false,
) {
    /** A disk a new volume, array or pool may use (and erase). */
    val free get() = usage == "free" && !system
    val title get() = listOf(vendor.trim(), model.trim()).filter { it.isNotEmpty() }.distinct().joinToString(" ").ifEmpty { name }
}
@Serializable data class SmartAttr(val id: Int = 0, val name: String = "", val value: Int = 0, val worst: Int = 0, val thresh: Int = 0, val raw: String = "", val failed: String = "")
@Serializable data class Smart(
    val device: String = "", val available: Boolean = false, val enabled: Boolean = false, val passed: Boolean? = null, val model: String = "",
    val serial: String = "", val firmware: String = "", val capacity: Long = 0, val temperature: Int = 0, val powerOnHours: Int = 0, val powerCycles: Int = 0,
    val attributes: List<SmartAttr>? = null, val messages: List<String>? = null, val warnings: List<String>? = null, val standby: Boolean = false,
)
@Serializable data class RaidMember(val device: String = "", val raidDevice: Int = 0, val state: String = "")
@Serializable data class RaidArray(
    val name: String = "", val device: String = "", val label: String = "", val level: String = "", val size: Long = 0, val state: String = "",
    val health: String = "", val raidDevices: Int = 0, val activeDevices: Int = 0, val failedDevices: Int = 0, val spareDevices: Int = 0,
    val members: List<RaidMember>? = null, val syncAction: String = "", val syncProgress: Double = 0.0, val syncFinish: String = "", val fsType: String = "", val usedBy: String = "",
)
@Serializable data class ZProblem(val device: String = "", val state: String = "", val was: String = "")
@Serializable data class ZDataset(
    val name: String = "", val type: String = "", val used: Long = 0, val avail: Long = 0, val refer: Long = 0, val mountpoint: String = "",
    val compression: String = "", val quota: Long = 0, val creation: Long = 0, val lockedUntil: Long = 0,
)
@Serializable data class ZPool(
    val name: String = "", val size: Long = 0, val alloc: Long = 0, val free: Long = 0, val fragmentation: String = "", val capacity: String = "",
    val health: String = "", val status: String = "", val problems: List<ZProblem>? = null, val datasets: List<ZDataset>? = null,
)
@Serializable data class ZfsInfo(val available: Boolean = false, val broken: Boolean = false, val pools: List<ZPool>? = null)
@Serializable data class Usage(val total: Long = 0, val used: Long = 0, val free: Long = 0)
@Serializable data class VolumeView(
    val id: String = "", val name: String = "", val devices: List<String>? = null, val fsType: String = "", val uuid: String = "", val mountPoint: String = "",
    val profile: String = "", val mounted: Boolean = false, val usage: Usage = Usage(), val shares: List<String>? = null, val health: String = "",
)
@Serializable data class BtrfsSubvol(val id: Int = 0, val path: String = "", val snapshot: Boolean = false, val readOnly: Boolean = false, val lockedUntil: Long = 0)
@Serializable data class BtrfsInfo(val mountPoint: String = "", val dataProfile: String = "", val metaProfile: String = "", val size: Long = 0, val used: Long = 0, val subvolumes: List<BtrfsSubvol>? = null, val scrub: String = "", val balance: String = "")
@Serializable data class StorageLocation(val path: String = "", val label: String = "", val free: Long = 0)
@Serializable data class QuotaEntry(
    val kind: String = "", val target: String = "", val name: String = "", val mountPoint: String = "", val enabled: Boolean = false,
    val supported: Boolean = false, val used: Long = 0, val limit: Long = 0,
)
@Serializable data class NasUser(val username: String = "", val fullName: String = "", val role: String = "", val disabled: Boolean = false)
@Serializable data class NasGroup(val name: String = "", val members: List<String>? = null, val system: Boolean = false)

@Serializable data class CreateVolume(
    val name: String, val fsType: String, val devices: List<String>, val dataProfile: String = "", val metaProfile: String = "", val compression: Boolean = false,
)
@Serializable data class CreateRaid(val name: String, val level: String, val devices: List<String>, val spares: List<String> = emptyList(), val chunk: Int = 0)
@Serializable data class VDev(val type: String, val disks: List<String>)
@Serializable data class CreatePool(val name: String, val vdevs: List<VDev>, val compression: String = "lz4", val ashift: Int = 0, val encryption: Boolean = false)

@Serializable private data class DeviceBody(val device: String = "", val type: String = "", val count: Int = 0)
@Serializable private data class SnapBody(val dataset: String, val name: String, val recursive: Boolean = false)
@Serializable private data class RollbackBody(val snapshot: String)
@Serializable private data class BtrfsBody(val name: String = "", val path: String = "")
@Serializable private data class QuotaBody(val kind: String, val target: String, val limit: Long)

private fun Api.post(path: String, b: okhttp3.RequestBody = "{}".toRequestBody(jsonType), vararg q: Pair<String, String>): JsonObject =
    call(request(url(path, *q)).post(b).build(), JsonObject.serializer())

private fun Api.delete(path: String, vararg q: Pair<String, String>): JsonObject = call(request(url(path, *q)).delete().build(), JsonObject.serializer())

fun Api.disks(): List<Disk> = call(get("/api/storage/disks"), ListSerializer(Disk.serializer()))
fun Api.smart(device: String): Smart = call(get("/api/storage/disks/smart", "device" to device), Smart.serializer())
fun Api.smartTest(device: String, type: String) { post("/api/storage/disks/smart-test", body(DeviceBody(device = device, type = type))) }
fun Api.volumes(): List<VolumeView> = call(get("/api/storage/volumes"), ListSerializer(VolumeView.serializer()))
fun Api.createVolume(v: CreateVolume): Job = call(request(url("/api/storage/volumes")).post(body(v)).build(), Job.serializer())
fun Api.deleteVolume(id: String, wipe: Boolean) { if (wipe) delete("/api/storage/volumes/$id", "wipe" to "1") else delete("/api/storage/volumes/$id") }
fun Api.btrfs(id: String): BtrfsInfo = call(get("/api/storage/volumes/$id/btrfs"), BtrfsInfo.serializer())
fun Api.btrfsAction(id: String, action: String, name: String = "", path: String = ""): JsonObject = post("/api/storage/volumes/$id/btrfs/$action", body(BtrfsBody(name, path)))
fun Api.raids(): List<RaidArray> = call(get("/api/storage/raid"), ListSerializer(RaidArray.serializer()))
fun Api.createRaid(r: CreateRaid): Job = call(request(url("/api/storage/raid")).post(body(r)).build(), Job.serializer())
fun Api.raidAction(name: String, action: String, device: String = "") { post("/api/storage/raid/$name/$action", body(DeviceBody(device = device))) }
fun Api.deleteRaid(name: String) { delete("/api/storage/raid/$name") }
fun Api.zfs(): ZfsInfo = call(get("/api/storage/zfs"), ZfsInfo.serializer())
fun Api.createPool(p: CreatePool): Job = call(request(url("/api/storage/zfs")).post(body(p)).build(), Job.serializer())
fun Api.poolAction(pool: String, action: String) { post("/api/storage/zfs/$pool/$action", body(DeviceBody())) }
fun Api.destroyPool(pool: String) { delete("/api/storage/zfs/$pool") }
fun Api.zfsSnapshots(): List<ZDataset> = call(get("/api/storage/zfs-snapshots"), ListSerializer(ZDataset.serializer()))
fun Api.zfsSnapshot(dataset: String, name: String, recursive: Boolean) { post("/api/storage/zfs-snapshots", body(SnapBody(dataset, name, recursive))) }
fun Api.zfsRollback(snapshot: String) { post("/api/storage/zfs-snapshots/rollback", body(RollbackBody(snapshot))) }
fun Api.zfsDestroy(name: String) { delete("/api/storage/zfs-datasets", "name" to name) }
fun Api.locations(): List<StorageLocation> = call(get("/api/storage/locations"), ListSerializer(StorageLocation.serializer()))

/** Full share objects: PUT replaces a share, so edits keep every field the NAS sent. */
fun Api.shareObjects(): List<JsonObject> = call(get("/api/shares"), ListSerializer(JsonObject.serializer()))
fun Api.createShare(share: JsonObject) { post("/api/shares", json.encodeToString(JsonObject.serializer(), share).toRequestBody(jsonType)) }
fun Api.saveShare(name: String, share: JsonObject) {
    call(request(url("/api/shares/${name.encodeSegment()}")).put(json.encodeToString(JsonObject.serializer(), share).toRequestBody(jsonType)).build(), JsonObject.serializer())
}
fun Api.deleteShare(name: String) { delete("/api/shares/${name.encodeSegment()}") }

fun Api.users(): List<NasUser> = call(get("/api/users"), ListSerializer(NasUser.serializer()))
fun Api.groups(): List<NasGroup> = call(get("/api/groups"), ListSerializer(NasGroup.serializer()))
fun Api.userQuotas(user: String): List<QuotaEntry> = call(get("/api/users/$user/quotas"), ListSerializer(QuotaEntry.serializer()))
fun Api.setUserQuota(user: String, kind: String, target: String, limit: Long) {
    call(request(url("/api/users/$user/quotas")).put(body(QuotaBody(kind, target, limit))).build(), JsonObject.serializer())
}
fun Api.enableVolumeQuota(id: String): Job = call(request(url("/api/storage/volumes/$id/quota")).post("{}".toRequestBody(jsonType)).build(), Job.serializer())

private fun String.encodeSegment(): String = java.net.URLEncoder.encode(this, "UTF-8").replace("+", "%20")

/** A new share object with the defaults of the web interface. */
fun newShare(name: String, path: String, description: String, smb: Boolean, nfs: Boolean, recycleBin: Boolean, permissions: Map<String, String>): JsonObject = buildJsonObject {
    put("name", name); put("path", path); put("description", description); put("smb", smb); put("nfs", nfs); put("guest", false)
    put("browseable", true); put("recycleBin", recycleBin); put("timeMachine", false)
    put("permissions", JsonObject(permissions.mapValues { JsonPrimitive(it.value) }))
    put("nfsClients", ""); put("nfsOptions", "")
}

/** [share] with some fields replaced. */
fun JsonObject.with(vararg fields: Pair<String, JsonElement>): JsonObject = JsonObject(this + fields)

fun JsonObject.str(key: String): String = (this[key] as? JsonPrimitive)?.content ?: ""
fun JsonObject.bool(key: String): Boolean = (this[key] as? JsonPrimitive)?.content == "true"
fun JsonObject.permissions(): Map<String, String> = (this["permissions"] as? JsonObject)?.mapValues { (it.value as? JsonPrimitive)?.content ?: "" } ?: emptyMap()
