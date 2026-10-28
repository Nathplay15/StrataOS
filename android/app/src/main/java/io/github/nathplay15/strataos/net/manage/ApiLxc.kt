package io.github.nathplay15.strataos.net.manage

import io.github.nathplay15.strataos.net.*

import java.time.OffsetDateTime
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer

// LXC system containers, next to the VMs (backend api/h_lxc.go). Administrators only.

@Serializable data class LxcImage(val distro: String = "", val release: String = "", val arch: String = "", val variant: String = "", val build: String = "", val label: String = "", val recommended: Boolean = false)
@Serializable data class LxcImageRef(val distro: String = "", val release: String = "", val arch: String = "", val variant: String = "", val build: String = "")
@Serializable data class LxcNetwork(val mode: String = "nat", val bridge: String = "", val mac: String = "", val address: String = "", val gateway: String = "", val dns: List<String> = emptyList())
@Serializable data class LxcMount(val share: String = "", val source: String = "", val target: String = "", val readOnly: Boolean = false)
@Serializable data class LxcFeatures(val nesting: Boolean = false, val fuse: Boolean = false, val tun: Boolean = false, val keyctl: Boolean = false)
@Serializable data class LxcStorage(val backend: String = "", val location: String = "", val base: String = "", val dataset: String = "", val sizeGb: Int = 0)

@Serializable data class Container(
    val name: String = "", val description: String = "", val created: String = "", val image: LxcImageRef = LxcImageRef(),
    val privileged: Boolean = false, val storage: LxcStorage = LxcStorage(), val cpus: Double = 0.0, val memoryMb: Int = 0, val swapMb: Int = 0,
    val network: LxcNetwork = LxcNetwork(), val mounts: List<LxcMount> = emptyList(), val features: LxcFeatures = LxcFeatures(),
    val autostart: Boolean = false, val autostartOrder: Int = 0, val autostartDelay: Int = 0,
    val state: String = "", val pid: Int = 0, val ips: List<String> = emptyList(), val snapshots: Int = 0,
    val memoryBytes: Long = 0, val cpuUsec: Long = 0, val missing: Boolean = false,
    val mapUsers: List<String> = emptyList(), val mapGroups: List<String> = emptyList(),
) {
    val running get() = state == "running"
    val stopped get() = state == "stopped"
}

@Serializable data class LxcList(val installed: Boolean = false, val containers: List<Container> = emptyList(), val bridges: List<String> = emptyList(), val natBridge: String = "", val arch: String = "")
@Serializable data class LxcTemplates(val images: List<LxcImage> = emptyList(), val updated: String = "", val server: String = "")
@Serializable data class LxcLocation(val path: String = "", val label: String = "", val fsType: String = "", val free: Long = 0, val backends: List<String> = emptyList())
@Serializable data class LxcSnapshot(val name: String = "", val description: String = "", val created: String = "", val running: Boolean = false, val backend: String = "")
@Serializable data class LxcArchive(val path: String = "", val container: String = "", val created: String = "", val size: Long = 0)
@Serializable data class LxcStats(
    val time: String = "", val state: String = "", val cpuUsec: Long = 0, val cpuLimit: Double = 0.0, val cores: Int = 0, val memory: Long = 0,
    val memoryMax: Long = 0, val swap: Long = 0, val swapMax: Long = 0, val pids: Long = 0, val netRx: Long = 0, val netTx: Long = 0,
    val diskRead: Long = 0, val diskWrite: Long = 0, val diskUsed: Long = 0, val diskLimit: Long = 0, val ips: List<String> = emptyList(),
)

/** The settings PUT /api/lxc/{name} takes. */
@Serializable data class LxcSettings(
    val description: String = "", val cpus: Double = 0.0, val memoryMb: Int = 0, val swapMb: Int = 0, val sizeGb: Int = 0,
    val network: LxcNetwork = LxcNetwork(), val mounts: List<LxcMount> = emptyList(), val features: LxcFeatures = LxcFeatures(),
    val autostart: Boolean = false, val autostartOrder: Int = 0, val autostartDelay: Int = 0,
    val mapUsers: List<String> = emptyList(), val mapGroups: List<String> = emptyList(),
)

/** A new container (POST /api/lxc). */
@Serializable data class LxcCreateReq(
    val name: String, val description: String = "", val image: LxcImageRef, val privileged: Boolean = false,
    val location: String, val backend: String, val sizeGb: Int, val cpus: Double, val memoryMb: Int, val swapMb: Int,
    val network: LxcNetwork, val mounts: List<LxcMount>, val features: LxcFeatures,
    val mapUsers: List<String> = emptyList(), val mapGroups: List<String> = emptyList(),
    val autostart: Boolean, val start: Boolean, val rootPassword: String = "",
)

@Serializable private data class LxcNameReq(val name: String)
@Serializable private data class SnapshotReq(val name: String, val description: String)
@Serializable private data class BackupReq(val dir: String, val mode: String, val keep: Int)
@Serializable data class RestartAnswer(val restart: Boolean = false)

/** The settings of a container as they are (the base of every change). */
fun settingsOf(c: Container) = LxcSettings(
    c.description, c.cpus, c.memoryMb, c.swapMb, c.storage.sizeGb, c.network,
    c.mounts.map { LxcMount(share = it.share, target = it.target, readOnly = it.readOnly) }, c.features,
    c.autostart, c.autostartOrder, c.autostartDelay, c.mapUsers, c.mapGroups,
)

/** Container names the NAS accepts (also the host name inside). */
val lxcNameRe = Regex("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
private val reservedLxc = setOf("templates", "storage", "archives", "import", "autostart", "shares", "users")
fun validLxcName(n: String) = lxcNameRe.matches(n) && n !in reservedLxc

/** Static addresses in CIDR form (empty: DHCP). */
fun validCidr(a: String): Boolean {
    if (a.isEmpty()) return true
    val m = Regex("^(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})/(\\d{1,2})$").find(a) ?: return false
    val g = m.groupValues.drop(1).map { it.toInt() }
    return g.take(4).all { it in 0..255 } && g[4] in 1..32
}

/** The power actions offered for a container in its state. */
fun lxcActions(c: Container): List<String> = when (c.state) {
    "running" -> listOf("stop", "restart", "kill")
    "stopped" -> if (c.missing) emptyList() else listOf("start")
    "frozen", "starting", "stopping" -> listOf("kill")
    else -> listOf("start", "kill")
}

/** The images shown in the wizard: the recommended ones, or all, or those matching [search]. */
fun filterImages(images: List<LxcImage>, search: String, all: Boolean): List<LxcImage> {
    val s = search.trim().lowercase()
    return images.filter { (all || it.recommended || s.isNotEmpty()) && (s.isEmpty() || it.label.lowercase().contains(s) || it.distro.contains(s)) }
}

/** CPU use between two samples, in % of one core (100 = one full core). */
fun lxcCpuPercent(prev: LxcStats?, cur: LxcStats): Double? {
    if (prev == null || cur.state != "running") return null
    val dt = runCatching { (OffsetDateTime.parse(cur.time).toInstant().toEpochMilli() - OffsetDateTime.parse(prev.time).toInstant().toEpochMilli()) / 1000.0 }.getOrNull() ?: return null
    if (dt <= 0) return null
    return (cur.cpuUsec - prev.cpuUsec).coerceAtLeast(0) / dt / 1e4
}

/** The default mount point of a shared folder in a container. */
fun mountTarget(share: String) = "/mnt/" + share.lowercase().replace(Regex("\\s+"), "-")

fun Api.lxc(): LxcList = call(get("/api/lxc"), LxcList.serializer())
fun Api.lxcGet(name: String): Container = call(get("/api/lxc/$name"), Container.serializer())
fun Api.lxcTemplates(refresh: Boolean = false): LxcTemplates = call(if (refresh) get("/api/lxc/templates", "refresh" to "1") else get("/api/lxc/templates"), LxcTemplates.serializer())
fun Api.lxcStorage(): List<LxcLocation> = call(get("/api/lxc/storage"), ListSerializer(LxcLocation.serializer()))
fun Api.lxcCreate(r: LxcCreateReq): NasJob = call(sendJson("POST", "/api/lxc", r), NasJob.serializer())
fun Api.lxcUpdate(name: String, s: LxcSettings): RestartAnswer = call(sendJson("PUT", "/api/lxc/$name", s), RestartAnswer.serializer())
fun Api.lxcDelete(name: String) = call(send("DELETE", "/api/lxc/$name"), okSer)
/** start, stop, restart, kill. */
fun Api.lxcAction(name: String, action: String) = call(send("POST", "/api/lxc/$name/$action"), okSer)
fun Api.lxcClone(name: String, to: String): NasJob = call(sendJson("POST", "/api/lxc/$name/clone", LxcNameReq(to)), NasJob.serializer())
fun Api.lxcRename(name: String, to: String) = call(sendJson("POST", "/api/lxc/$name/rename", LxcNameReq(to)), okSer)
fun Api.lxcStats(name: String): LxcStats = call(get("/api/lxc/$name/stats", "disk" to "1"), LxcStats.serializer())
fun Api.lxcSnapshots(name: String): List<LxcSnapshot> = call(get("/api/lxc/$name/snapshots"), ListSerializer(LxcSnapshot.serializer()))
fun Api.lxcSnapshot(name: String, snap: String, description: String): NasJob = call(sendJson("POST", "/api/lxc/$name/snapshots", SnapshotReq(snap, description)), NasJob.serializer())
fun Api.lxcRestore(name: String, snap: String): NasJob = call(send("POST", "/api/lxc/$name/snapshots/$snap/restore"), NasJob.serializer())
fun Api.lxcDeleteSnapshot(name: String, snap: String) = call(send("DELETE", "/api/lxc/$name/snapshots/$snap"), okSer)
fun Api.lxcBackups(name: String, dir: String): List<LxcArchive> = call(get("/api/lxc/$name/backups", "dir" to dir), ListSerializer(LxcArchive.serializer()))
fun Api.lxcBackup(name: String, dir: String, mode: String, keep: Int): NasJob = call(sendJson("POST", "/api/lxc/$name/backup", BackupReq(dir, mode, keep)), NasJob.serializer())

/** The websocket of a container terminal: mode "shell" (root, lxc-attach) or "console" (login prompt). */
fun Api.lxcConsoleUrl(name: String, mode: String) = url("/api/lxc/$name/console", "mode" to mode)
