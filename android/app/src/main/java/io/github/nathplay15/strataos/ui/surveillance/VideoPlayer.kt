package io.github.nathplay15.strataos.ui.surveillance

import android.media.MediaCodecList
import android.view.ViewGroup
import androidx.annotation.OptIn
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.DefaultLoadControl
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.ProgressiveMediaSource
import androidx.media3.ui.AspectRatioFrameLayout
import androidx.media3.ui.PlayerView
import io.github.nathplay15.strataos.net.authHeaders
import io.github.nathplay15.strataos.ui.adminkit.api
import kotlinx.coroutines.delay
import okhttp3.HttpUrl

/** Whether the phone decodes H.265 (recordings are then sent as they are). */
val decodesHevc: Boolean by lazy {
    runCatching { MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.any { !it.isEncoder && it.supportedTypes.any { t -> t.equals("video/hevc", true) } } }.getOrDefault(false)
}

/**
 * How far behind live a live view may fall: past [LIVE_SPEED_UP] it plays a
 * little faster, past [LIVE_JUMP] it jumps to the newest video.
 */
const val LIVE_SPEED_UP = 1500L
const val LIVE_JUMP = 5000L

/** Playback speed that brings a live view back close to the newest video it has. */
fun catchUpSpeed(lagMs: Long, current: Float): Float = when {
    lagMs > LIVE_SPEED_UP -> 1.15f
    lagMs < 500 -> 1f
    else -> current
}

/**
 * Plays a video of the NAS with the device token: a recording, a clip, or the
 * live stream ([live]: small buffers, no controls, kept close to live).
 */
@OptIn(UnstableApi::class)
@Composable
fun NasVideo(
    url: HttpUrl, live: Boolean, modifier: Modifier = Modifier, startMs: Long = 0, onError: (PlaybackException) -> Unit = {},
    onReady: () -> Unit = {}, position: ((Long) -> Unit)? = null,
) {
    val ctx = LocalContext.current
    val player = remember(url.toString()) {
        val api = api()
        val ds = OkHttpDataSource.Factory(api.http).setDefaultRequestProperties(api.authHeaders())
        val b = ExoPlayer.Builder(ctx).setMediaSourceFactory(ProgressiveMediaSource.Factory(ds))
        if (live) b.setLoadControl(DefaultLoadControl.Builder().setBufferDurationsMs(500, 3000, 200, 500).build())
        b.build().apply {
            setMediaItem(MediaItem.fromUri(url.toString()), startMs)
            playWhenReady = true
            prepare()
        }
    }
    DisposableEffect(player) {
        val l = object : Player.Listener {
            override fun onPlayerError(error: PlaybackException) = onError(error)
            override fun onPlaybackStateChanged(state: Int) { if (state == Player.STATE_READY) onReady() }
        }
        player.addListener(l)
        onDispose { player.removeListener(l); player.release() }
    }
    // Paused in the background. A live view lets the NAS stop its stream
    // meanwhile, and starts again from the newest video.
    val owner = LocalLifecycleOwner.current
    DisposableEffect(owner, player) {
        var stopped = false
        val obs = LifecycleEventObserver { _, e ->
            when (e) {
                Lifecycle.Event.ON_STOP -> if (live) { player.stop(); stopped = true } else player.pause()
                Lifecycle.Event.ON_START -> if (live && stopped) {
                    stopped = false
                    player.setMediaItem(MediaItem.fromUri(url.toString()))
                    player.prepare()
                    player.play()
                }
                else -> {}
            }
        }
        owner.lifecycle.addObserver(obs)
        onDispose { owner.lifecycle.removeObserver(obs) }
    }
    if (live) LaunchedEffect(player) {
        while (true) {
            delay(500)
            if (!player.isPlaying) continue
            val lag = player.bufferedPosition - player.currentPosition
            if (lag > LIVE_JUMP) player.seekTo(player.bufferedPosition - 300)
            val speed = catchUpSpeed(lag, player.playbackParameters.speed)
            if (speed != player.playbackParameters.speed) player.setPlaybackSpeed(speed)
        }
    }
    if (position != null) LaunchedEffect(player) {
        while (true) {
            position(player.currentPosition)
            delay(500)
        }
    }
    AndroidView(
        modifier = modifier,
        factory = { c ->
            PlayerView(c).apply {
                layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                useController = !live
                resizeMode = AspectRatioFrameLayout.RESIZE_MODE_FIT
                setShowBuffering(PlayerView.SHOW_BUFFERING_ALWAYS)
                this.player = player
            }
        },
        update = { it.player = player },
    )
}
