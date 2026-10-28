package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.serializer

// Virtual machines (backend api/h_vms.go, h_vms_more.go). Administrators only.

@Serializable data class VmDisk(val device: String = "", val target: String = "", val bus: String = "", val source: String = "", val size: Long = 0)
@Serializable data class VmNic(val type: String = "", val source: String = "", val mac: String = "", val model: String = "", val ip: String = "")

@Serializable data class Vm(
    val name: String = "", val uuid: String = "", val state: String = "", val vcpus: Int = 0, val memoryMb: Int = 0,
    val autostart: Boolean = false, val firmware: String = "", val os: String = "", val disks: List<VmDisk> = emptyList(),
    val nics: List<VmNic> = emptyList(), val vncPort: Int = 0, val dir: String = "", val template: Boolean = false,
    val cloud: String = "", val snapshots: Int = 0, val autostartOrder: Int = 0, val autostartDelay: Int = 0,
) {
    val running get() = state == "running"
    val paused get() = state == "paused"
    val off get() = state == "shut off" || state == "shutoff"
    val diskBytes get() = disks.filter { it.device == "disk" }.sumOf { it.size }
    val iso get() = disks.firstOrNull { it.device == "cdrom" }?.source ?: ""
}

@Serializable data class VmList(
    val installed: Boolean = false, val kvm: Boolean = false, val vms: List<Vm> = emptyList(),
    /** CPU time (ns) and memory in use (KiB) of the running VMs. */
    val stats: Map<String, List<Long>> = emptyMap(), val bridges: List<String>? = null,
)

@Serializable data class VmSnapshot(
    val name: String = "", val description: String = "", val created: String = "", val state: String = "",
    val memory: Boolean = false, val kind: String = "", val parent: String = "", val current: Boolean = false,
)

@Serializable data class CloudImage(val id: String = "", val name: String = "", val file: String = "", val cached: Boolean = false)

@Serializable data class VmCreateReq(
    val name: String, val os: String, val vcpus: Int, val memoryMb: Int, val diskGb: Int, val iso: String = "",
    val storage: String, val network: String, val firmware: String, val autostart: Boolean,
)

@Serializable data class CloudInit(
    val hostname: String = "", val user: String = "", val password: String = "", val sshKeys: List<String> = emptyList(),
    val packages: List<String> = emptyList(), val address: String = "", val gateway: String = "", val dns: List<String> = emptyList(),
)

@Serializable data class VmCloudReq(
    val name: String, val os: String = "linux", val vcpus: Int, val memoryMb: Int, val diskGb: Int, val storage: String,
    val network: String, val firmware: String, val autostart: Boolean, val image: String, val cloudInit: CloudInit,
)

@Serializable private data class VmActionReq(val iso: String = "", val autostart: Boolean = false, val vcpus: Int = 0, val memoryMb: Int = 0, val order: Int = 0, val delay: Int = 0)
@Serializable private data class SnapReq(val name: String, val description: String)
@Serializable data class VolumeUsage(val total: Long = 0, val used: Long = 0, val free: Long = 0)
@Serializable data class StorageVolume(val id: String = "", val name: String = "", val mountPoint: String = "", val mounted: Boolean = false, val usage: VolumeUsage = VolumeUsage())

fun Api.vms(): VmList = call(get("/api/vms"), VmList.serializer())
fun Api.vmIsos(): List<String> = call(get("/api/vms/isos"), ListSerializer(String.serializer()))
fun Api.cloudImages(): List<CloudImage> = call(get("/api/vms/cloud-images"), ListSerializer(CloudImage.serializer()))
fun Api.adminSshKeys(): List<String> = call(get("/api/vms/ssh-keys"), ListSerializer(String.serializer()))
fun Api.storageVolumes(): List<StorageVolume> = call(get("/api/storage/volumes"), ListSerializer(StorageVolume.serializer()))

/** start, shutdown (ACPI), destroy (power off), reboot, suspend, resume. */
fun Api.vmAction(name: String, action: String) = call(sendJson("POST", "/api/vms/$name/$action", VmActionReq()), okSer)
fun Api.vmResources(name: String, vcpus: Int, memoryMb: Int) = call(sendJson("POST", "/api/vms/$name/resources", VmActionReq(vcpus = vcpus, memoryMb = memoryMb)), okSer)
fun Api.vmMedia(name: String, iso: String) = call(sendJson("POST", "/api/vms/$name/media", VmActionReq(iso = iso)), okSer)
fun Api.vmAutostart(name: String, on: Boolean) = call(sendJson("POST", "/api/vms/$name/autostart", VmActionReq(autostart = on)), okSer)
fun Api.deleteVm(name: String, disks: Boolean) = call(send("DELETE", "/api/vms/$name", null, *(if (disks) arrayOf("disks" to "1") else emptyArray())), okSer)
fun Api.createVm(r: VmCreateReq): NasJob = call(sendJson("POST", "/api/vms", r), NasJob.serializer())
fun Api.createCloudVm(r: VmCloudReq): NasJob = call(sendJson("POST", "/api/vms/cloud", r), NasJob.serializer())

fun Api.vmSnapshots(name: String): List<VmSnapshot> = call(get("/api/vms/$name/snapshots"), ListSerializer(VmSnapshot.serializer()))
fun Api.createSnapshot(vm: String, name: String, description: String) = call(sendJson("POST", "/api/vms/$vm/snapshots", SnapReq(name, description)), okSer)
fun Api.revertSnapshot(vm: String, snap: String) = ok("POST", "/api/vms/$vm/snapshots/$snap/revert")
fun Api.deleteSnapshot(vm: String, snap: String) = ok("DELETE", "/api/vms/$vm/snapshots/$snap")

/** The websocket of the VM console (RFB relayed to the VM's VNC server). */
fun Api.vncUrl(name: String) = url("/api/vms/$name/vnc")

/** The VM names the NAS accepts. */
val vmNameRe = Regex("^[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}$")
