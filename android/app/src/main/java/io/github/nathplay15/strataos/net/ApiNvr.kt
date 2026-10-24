package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import okhttp3.HttpUrl

// Surveillance (backend api/h_nvr.go, h_nvr_live.go). Administrators only.

@Serializable data class CamStatus(val state: String = "", val since: String = "", val error: String = "", val restarts: Int = 0)

@Serializable data class Camera(
    val id: String = "", val name: String = "", val url: String = "", val subUrl: String = "",
    val username: String = "", val password: String = "", val enabled: Boolean = false, val engine: String = "",
    val audio: Boolean = false, val keepDays: Int = 0, val maxGb: Double = 0.0,
    val status: CamStatus = CamStatus(), val size: Long = 0, val segments: Int = 0, val oldest: String? = null,
    val frigateName: String = "",
) {
    val frigate get() = engine == "frigate"
    /** RTSP cameras have live video; HTTP ones (MJPEG…) only pictures. */
    val liveVideo get() = url.startsWith("rtsp://") || url.startsWith("rtsps://")
}

@Serializable data class NvrVolume(val path: String = "", val label: String = "", val free: Long = 0)

@Serializable data class FrigateView(
    val installed: Boolean = false, val docker: Boolean = false, val detector: String = "", val resolved: String = "",
    val intelGpu: String = "", val coral: Boolean = false, val objects: List<String> = emptyList(), val allObjects: List<String> = emptyList(),
    val notify: Boolean = false, val notifyMinutes: Int = 0, val cameras: Int = 0, val running: Boolean = false,
    val webPort: String = "", val error: String = "",
)

@Serializable data class Nvr(
    val installed: Boolean = false, val folder: String = "", val folderError: String = "", val alertMinutes: Int = 0,
    val segmentSeconds: Int = 0, val cameras: List<Camera> = emptyList(), val volumes: List<NvrVolume>? = null,
    val frigate: FrigateView = FrigateView(),
)

@Serializable data class Segment(val name: String = "", val start: String = "", val end: String = "", val size: Long = 0, val open: Boolean = false)
@Serializable data class Recordings(val days: List<String> = emptyList(), val day: String = "", val segments: List<Segment> = emptyList())
@Serializable data class SegmentInfo(val video: String = "", val audio: String = "", val duration: Double = 0.0, val mode: String = "", val direct: Boolean = false)
@Serializable data class FrigateEvent(val id: String = "", val label: String = "", val score: Double = 0.0, val start: String = "", val end: String? = null)
@Serializable data class FrigateEvents(val installed: Boolean = false, val day: String = "", val events: List<FrigateEvent> = emptyList(), val error: String = "")

@Serializable data class CameraReq(
    val name: String, val url: String, val subUrl: String = "", val username: String = "", val password: String = "",
    val enabled: Boolean = true, val engine: String = "", val audio: Boolean = false, val keepDays: Int = 0, val maxGb: Double = 0.0,
    val id: String = "", val stream: String = "",
)

@Serializable data class NvrSettingsReq(val folder: String, val alertMinutes: Int, val segmentSeconds: Int = 0)
@Serializable data class FrigateReq(val detector: String, val objects: List<String>, val notify: Boolean, val notifyMinutes: Int)
@Serializable data class FrigateConfig(val config: String = "", val detector: String = "")
@Serializable data class CleanupResult(val deleted: Int = 0)

fun Api.nvr(): Nvr = call(get("/api/nvr"), Nvr.serializer())
fun Api.saveNvrSettings(r: NvrSettingsReq) = call(sendJson("PUT", "/api/nvr/settings", r), okSer)
fun Api.addCamera(r: CameraReq): Camera = call(sendJson("POST", "/api/nvr/cameras", r), Camera.serializer())
fun Api.saveCamera(id: String, r: CameraReq) = call(sendJson("PUT", "/api/nvr/cameras/$id", r), okSer)
fun Api.deleteCamera(id: String, recordings: Boolean) =
    call(send("DELETE", "/api/nvr/cameras/$id", null, *(if (recordings) arrayOf("recordings" to "1") else emptyArray())), okSer)

/** A picture of the stream of a camera being edited (JPEG). */
fun Api.testCamera(r: CameraReq): ByteArray = postBytes("/api/nvr/test", json.encodeToString(CameraReq.serializer(), r))
fun Api.livePicture(id: String): ByteArray = bytes(url("/api/nvr/cameras/$id/live.jpg", "t" to System.currentTimeMillis().toString()))
fun Api.liveVideoUrl(id: String, main: Boolean): HttpUrl = url("/api/nvr/cameras/$id/live.mp4", "stream" to if (main) "main" else "sub")
fun Api.recordings(id: String, day: String?): Recordings =
    call(get("/api/nvr/cameras/$id/recordings", *(if (day != null) arrayOf("day" to day) else emptyArray())), Recordings.serializer())

fun Api.segmentInfo(id: String, name: String, hevc: Boolean): SegmentInfo =
    call(get("/api/nvr/cameras/$id/recordings/$name/info", *(if (hevc) arrayOf("hevc" to "1") else emptyArray())), SegmentInfo.serializer())

fun Api.segmentUrl(id: String, name: String): HttpUrl = url("/api/nvr/cameras/$id/recordings/$name")
fun Api.segmentPlayUrl(id: String, name: String, start: Int, hevc: Boolean): HttpUrl =
    url("/api/nvr/cameras/$id/recordings/$name/play", "start" to start.toString(), "hevc" to if (hevc) "1" else "0")
fun Api.clipUrl(id: String, name: String, from: Int, to: Int): HttpUrl =
    url("/api/nvr/cameras/$id/recordings/$name/clip", "from" to from.toString(), "to" to to.toString())

fun Api.frigateEvents(id: String, day: String): FrigateEvents = call(get("/api/nvr/cameras/$id/frigate/events", "day" to day), FrigateEvents.serializer())
fun Api.frigateFileUrl(id: String, event: String, file: String): HttpUrl = url("/api/nvr/cameras/$id/frigate/events/$event/$file")
fun Api.saveFrigate(r: FrigateReq) = call(sendJson("PUT", "/api/nvr/frigate", r), okSer)
fun Api.frigateConfig(): FrigateConfig = call(get("/api/nvr/frigate/config"), FrigateConfig.serializer())
fun Api.installFrigate(): NasJob = startJob("POST", "/api/nvr/frigate/install")
fun Api.nvrCleanup(): CleanupResult = call(send("POST", "/api/nvr/cleanup"), CleanupResult.serializer())
fun Api.installComponent(name: String): NasJob = startJob("POST", "/api/system/components/$name/install")
