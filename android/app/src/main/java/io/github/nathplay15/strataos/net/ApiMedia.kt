package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import okhttp3.HttpUrl

// Streaming of the NAS (backend/internal/mediaplay): /api/files/probe says how
// a file plays (native file with Range requests, remux or transcode to
// fragmented MP4 from start=), /api/files/play serves it, /api/files/subtitle
// turns a text subtitle track into WebVTT shifted to the same start.

@Serializable data class MediaTrack(
    val index: Int = 0, val type: String = "", val codec: String = "", val width: Int = 0, val height: Int = 0,
    val channels: Int = 0, val lang: String = "", val title: String = "", val default: Boolean = false,
)
@Serializable data class PlayPlan(
    val mode: String = "native", val audioOnly: Boolean = false, val video: MediaTrack? = null, val audio: MediaTrack? = null,
    val copyVideo: Boolean = false, val copyAudio: Boolean = false, val type: String = "", val height: Int = 0,
)
@Serializable data class Probe(
    val installed: Boolean = false, val plan: PlayPlan? = null, val duration: Double = 0.0, val format: String = "",
    val audioTracks: List<MediaTrack> = emptyList(), val subtitles: List<MediaTrack> = emptyList(),
)

/** What the phone asks for: [height] 0 = the best the NAS chooses, [original] = the file itself. */
data class PlayOptions(val audio: Int = -1, val height: Int = 0, val hevc: Boolean = false, val av1: Boolean = false, val original: Boolean = false)

private fun PlayOptions.query(): List<Pair<String, String>> = buildList {
    if (audio >= 0) add("audio" to audio.toString())
    if (height > 0) add("height" to height.toString())
    if (hevc) add("hevc" to "1")
    if (av1) add("av1" to "1")
}

fun Api.probe(path: String, o: PlayOptions): Probe =
    call(get("/api/files/probe", *(listOf("path" to path) + o.query()).toTypedArray()), Probe.serializer())

/**
 * The address the player opens. A remux or transcode starts at [startSec]
 * (seeking means a new request); a native file is seekable by itself.
 */
fun Api.playUrl(path: String, o: PlayOptions, startSec: Double): HttpUrl = when {
    o.original -> url("/api/files/download", "path" to path)
    else -> url("/api/files/play", *(listOf("path" to path) + o.query() + startParam(startSec)).toTypedArray())
}

fun Api.subtitleUrl(path: String, index: Int, startSec: Double): HttpUrl =
    url("/api/files/subtitle", *(listOf("path" to path, "index" to index.toString()) + startParam(startSec)).toTypedArray())

private fun startParam(s: Double): List<Pair<String, String>> = if (s > 0.5) listOf("start" to "%.3f".format(java.util.Locale.ROOT, s)) else emptyList()

/** Whether the player must restart the stream to seek (the NAS sends a live stream from start=). */
fun PlayPlan.restartToSeek(o: PlayOptions): Boolean = !o.original && mode != "native"
