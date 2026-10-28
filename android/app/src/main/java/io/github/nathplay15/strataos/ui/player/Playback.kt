package io.github.nathplay15.strataos.ui.player

import android.content.Context
import android.media.MediaCodecList
import android.net.Uri
import androidx.annotation.OptIn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.content.edit
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.common.MimeTypes
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.net.PlayOptions
import io.github.nathplay15.strataos.net.Probe
import io.github.nathplay15.strataos.net.playUrl
import io.github.nathplay15.strataos.net.probe
import io.github.nathplay15.strataos.net.restartToSeek
import io.github.nathplay15.strataos.net.subtitleUrl
import java.io.IOException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.Call

/** A file of the play queue. */
data class QueueItem(val path: String, val name: String, val video: Boolean)

/**
 * The player of the app, shared by the player screen and the media session
 * service (background music, notification, lock screen). Files stream from
 * the NAS: a native file is seekable by itself; a remux or transcode is a
 * stream from start=, so seeking replaces the item with one starting there
 * ([offsetSec] is where the current stream starts in the file).
 */
@OptIn(UnstableApi::class)
object Playback {
    var player: ExoPlayer? = null
        private set
    var queue by mutableStateOf<List<QueueItem>>(emptyList())
        private set
    var probe by mutableStateOf<Probe?>(null)
        private set
    var options by mutableStateOf(PlayOptions())
        private set
    var subtitle by mutableStateOf<Int?>(null) // NAS subtitle track (streams only)
        private set
    var error by mutableStateOf<String?>(null)
    private var offsetSec = 0.0
    private var currentPath = ""
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val caps by lazy { codecCaps() }

    /** The NAS client with the device token, for ExoPlayer (resolved per request: sign-in may change). */
    private val callFactory = Call.Factory { req ->
        val api = Graph.api() ?: throw IOException("signed out")
        val signed = api.request(req.url).build()
        api.http.newCall(req.newBuilder().apply { signed.headers.forEach { (k, v) -> header(k, v) } }.build())
    }

    fun ensurePlayer(ctx: Context): ExoPlayer = player ?: ExoPlayer.Builder(ctx.applicationContext)
        .setMediaSourceFactory(DefaultMediaSourceFactory(OkHttpDataSource.Factory(callFactory)))
        .setAudioAttributes(AudioAttributes.Builder().setUsage(C.USAGE_MEDIA).setContentType(C.AUDIO_CONTENT_TYPE_MOVIE).build(), true)
        .setHandleAudioBecomingNoisy(true)
        .setWakeMode(C.WAKE_MODE_NETWORK)
        .build().also { p ->
            p.addListener(object : Player.Listener {
                override fun onMediaItemTransition(item: MediaItem?, reason: Int) {
                    val path = item?.mediaId ?: return
                    if (path != currentPath) {
                        saveResume()
                        currentPath = path
                        offsetSec = 0.0
                        subtitle = null
                        probe = null
                        options = options.copy(audio = -1) // track numbers are per file
                        loadProbe(path)
                    }
                }

                override fun onPlayerError(e: PlaybackException) {
                    error = e.message ?: e.errorCodeName
                }

                override fun onIsPlayingChanged(isPlaying: Boolean) {
                    if (!isPlaying) saveResume()
                }
            })
            player = p
        }

    val current: QueueItem? get() = queue.firstOrNull { it.path == currentPath }

    /** Plays [items] from [index]; a long video starts where it was left. */
    fun open(ctx: Context, items: List<QueueItem>, index: Int) {
        val p = ensurePlayer(ctx)
        saveResume()
        error = null
        queue = items
        options = PlayOptions(hevc = caps.first, av1 = caps.second)
        val first = items[index]
        currentPath = first.path
        offsetSec = 0.0
        subtitle = null
        probe = null
        scope.launch {
            val pr = runCatching { withContext(Dispatchers.IO) { Graph.api()!!.probe(first.path, options) } }.getOrNull()
            probe = pr
            val resume = resumeFor(first.path, pr?.duration ?: 0.0)
            val restart = pr?.plan?.restartToSeek(options) == true
            val start = if (restart) resume else 0.0
            offsetSec = start
            p.setMediaItems(items.mapIndexed { i, it -> mediaItem(it, if (i == index) start else 0.0) }, index, if (!restart) (resume * 1000).toLong() else 0L)
            p.prepare()
            p.play()
        }
    }

    private fun loadProbe(path: String) {
        scope.launch {
            val pr = runCatching { withContext(Dispatchers.IO) { Graph.api()!!.probe(path, options) } }.getOrNull()
            if (path == currentPath) probe = pr
        }
    }

    private fun mediaItem(it: QueueItem, startSec: Double, sub: Int? = null): MediaItem {
        val api = Graph.api()!!
        val b = MediaItem.Builder().setMediaId(it.path).setUri(Uri.parse(api.playUrl(it.path, options, startSec).toString()))
            .setMediaMetadata(MediaMetadata.Builder().setTitle(it.name).setDisplayTitle(it.name).setArtist(folderOf(it.path)).build())
        if (sub != null && !options.original) {
            b.setSubtitleConfigurations(listOf(
                MediaItem.SubtitleConfiguration.Builder(Uri.parse(api.subtitleUrl(it.path, sub, startSec).toString()))
                    .setMimeType(MimeTypes.TEXT_VTT).setSelectionFlags(C.SELECTION_FLAG_DEFAULT).setId("nas-$sub").build(),
            ))
        }
        return b.build()
    }

    /** Position in the file, in seconds (stream offset included). */
    fun positionSec(): Double = offsetSec + (player?.currentPosition ?: 0) / 1000.0

    fun durationSec(): Double {
        val pr = probe?.duration ?: 0.0
        if (pr > 0) return pr
        val d = player?.duration ?: C.TIME_UNSET
        return if (d == C.TIME_UNSET || d <= 0) 0.0 else d / 1000.0
    }

    private val restart get() = probe?.plan?.restartToSeek(options) == true

    fun seekTo(sec: Double) {
        val p = player ?: return
        val target = sec.coerceIn(0.0, maxOf(0.0, durationSec() - 1))
        if (restart) reload(target) else p.seekTo((target * 1000).toLong())
    }

    /** Replaces the current item with one starting at [startSec] (stream seek, track or quality change). */
    private fun reload(startSec: Double) {
        val p = player ?: return
        val item = current ?: return
        val idx = p.currentMediaItemIndex
        val streams = probe?.plan?.restartToSeek(options) == true
        offsetSec = if (streams) startSec else 0.0
        p.replaceMediaItem(idx, mediaItem(item, offsetSec, subtitle))
        p.seekTo(idx, if (streams) 0 else (startSec * 1000).toLong())
        p.prepare()
        p.play()
    }

    /** Changes the options (quality, audio track) and goes on from the same place. */
    fun changeOptions(o: PlayOptions) {
        val at = positionSec()
        options = o
        val path = currentPath
        scope.launch {
            probe = runCatching { withContext(Dispatchers.IO) { Graph.api()!!.probe(path, o) } }.getOrNull()
            reload(at)
        }
    }

    fun selectSubtitle(index: Int?) {
        subtitle = index
        player?.let { p ->
            p.trackSelectionParameters = p.trackSelectionParameters.buildUpon().setTrackTypeDisabled(C.TRACK_TYPE_TEXT, index == null).build()
        }
        if (!options.original) reload(positionSec())
    }

    /** Embedded tracks the player itself sees (when playing the original file). */
    fun selectEmbedded(type: Int, group: Int, track: Int) {
        val p = player ?: return
        val g = p.currentTracks.groups.filter { it.type == type }.getOrNull(group) ?: return
        p.trackSelectionParameters = p.trackSelectionParameters.buildUpon()
            .setTrackTypeDisabled(type, false)
            .setOverrideForType(TrackSelectionOverride(g.mediaTrackGroup, track)).build()
    }

    fun disableText() {
        val p = player ?: return
        p.trackSelectionParameters = p.trackSelectionParameters.buildUpon().setTrackTypeDisabled(C.TRACK_TYPE_TEXT, true).build()
    }

    fun stop() {
        saveResume()
        player?.stop()
        player?.clearMediaItems()
        queue = emptyList()
        currentPath = ""
    }

    fun release() {
        saveResume()
        player?.release()
        player = null
        queue = emptyList()
        currentPath = ""
    }

    // ---- resume positions ----

    private val prefs get() = Graph.app.getSharedPreferences("resume", Context.MODE_PRIVATE)

    private fun resumeFor(path: String, duration: Double): Double =
        resumePosition(prefs.getFloat(path, 0f).toDouble(), duration)

    private fun saveResume() {
        val path = currentPath
        if (path.isEmpty() || player == null) return
        val d = durationSec()
        val pos = positionSec()
        prefs.edit {
            if (keepResume(pos, d)) putFloat(path, pos.toFloat()) else remove(path)
            // Keep the list bounded (a few hundred files are plenty).
            val all = prefs.all
            if (all.size > 200) all.keys.take(all.size - 200).forEach { remove(it) }
        }
    }

    /** HEVC and AV1 decoders of this phone (sent to the NAS, which then copies those videos instead of transcoding). */
    private fun codecCaps(): Pair<Boolean, Boolean> = runCatching {
        val infos = MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.filter { !it.isEncoder }
        fun has(mime: String) = infos.any { i -> i.supportedTypes.any { it.equals(mime, ignoreCase = true) } }
        has(MimeTypes.VIDEO_H265) to has(MimeTypes.VIDEO_AV1)
    }.getOrDefault(false to false)
}

private fun folderOf(path: String) = path.substringBeforeLast('/').substringAfterLast('/')

/** Where to start again: only for long files, not at the very start or end. */
fun resumePosition(saved: Double, duration: Double): Double =
    if (saved > 10 && (duration <= 0 || saved < duration - 15)) saved else 0.0

/** Whether a position is worth keeping (a long file, left in the middle). */
fun keepResume(pos: Double, duration: Double): Boolean = duration > 120 && pos > 10 && pos < duration * 0.95
