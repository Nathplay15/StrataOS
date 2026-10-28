package io.github.nathplay15.strataos.ui.player

import android.app.PictureInPictureParams
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.res.Configuration
import android.os.Build
import android.os.Bundle
import android.util.Rational
import android.view.ViewGroup
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.annotation.OptIn
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Forward10
import androidx.compose.material.icons.filled.MusicNote
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PictureInPictureAlt
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Replay10
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.SkipNext
import androidx.compose.material.icons.filled.SkipPrevious
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Slider
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableDoubleStateOf
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.media3.common.C
import androidx.media3.common.Player
import androidx.media3.common.util.UnstableApi
import androidx.media3.session.MediaController
import androidx.media3.session.SessionToken
import androidx.media3.ui.AspectRatioFrameLayout
import androidx.media3.ui.PlayerView
import com.google.common.util.concurrent.ListenableFuture
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.MediaTrack
import io.github.nathplay15.strataos.ui.StrataTheme
import kotlinx.coroutines.delay

/** Full-screen player for videos and music of the NAS, with picture-in-picture. */
class PlayerActivity : ComponentActivity() {
    private var inPip by mutableStateOf(false)
    private var controller: ListenableFuture<MediaController>? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        WindowCompat.getInsetsController(window, window.decorView).apply {
            hide(WindowInsetsCompat.Type.systemBars())
            systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        }
        if (Playback.player == null || Playback.queue.isEmpty()) {
            finish() // reopened from the notification after playback ended
            return
        }
        setContent {
            StrataTheme {
                PlayerScreen(inPip, onClose = ::close, onPip = ::enterPip)
            }
        }
    }

    /** Back: a video stops; music goes on in the background (notification). */
    private fun close() {
        if (Playback.current?.video != false || Playback.player?.isPlaying != true) Playback.release()
        finish()
    }

    override fun onStart() {
        super.onStart()
        // Binding to the session service keeps music going after the screen closes.
        controller = MediaController.Builder(this, SessionToken(this, ComponentName(this, PlaybackService::class.java))).buildAsync()
        updatePipParams()
    }

    override fun onStop() {
        super.onStop()
        controller?.let { MediaController.releaseFuture(it) }
        controller = null
        // A video stops with its window (picture-in-picture closed included); music goes on.
        if (Playback.current?.video == true) Playback.player?.pause()
    }

    override fun onUserLeaveHint() {
        super.onUserLeaveHint()
        if (Build.VERSION.SDK_INT < 31 && Playback.current?.video == true && Playback.player?.isPlaying == true) enterPip()
    }

    override fun onPictureInPictureModeChanged(isInPictureInPictureMode: Boolean, newConfig: Configuration) {
        super.onPictureInPictureModeChanged(isInPictureInPictureMode, newConfig)
        inPip = isInPictureInPictureMode
    }

    private fun pipParams(): PictureInPictureParams {
        val v = Playback.player?.videoSize
        val b = PictureInPictureParams.Builder()
        if (v != null && v.width > 0 && v.height > 0) {
            val r = v.width.toFloat() / v.height
            if (r in 0.42f..2.39f) b.setAspectRatio(Rational(v.width, v.height))
        }
        if (Build.VERSION.SDK_INT >= 31) b.setAutoEnterEnabled(Playback.current?.video == true && Playback.player?.isPlaying == true)
        return b.build()
    }

    fun updatePipParams() {
        if (packageManager.hasSystemFeature("android.software.picture_in_picture")) runCatching { setPictureInPictureParams(pipParams()) }
    }

    private fun enterPip() {
        if (packageManager.hasSystemFeature("android.software.picture_in_picture")) runCatching { enterPictureInPictureMode(pipParams()) }
    }

    companion object {
        /** Plays [items] from [index] in the full-screen player. */
        fun start(ctx: Context, items: List<QueueItem>, index: Int) {
            Playback.open(ctx, items, index)
            ctx.startActivity(Intent(ctx, PlayerActivity::class.java))
        }
    }
}

private fun fmt(sec: Double): String {
    val s = sec.toLong().coerceAtLeast(0)
    return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s / 60 % 60, s % 60) else "%d:%02d".format(s / 60, s % 60)
}

@OptIn(UnstableApi::class)
@Composable
private fun PlayerScreen(inPip: Boolean, onClose: () -> Unit, onPip: () -> Unit) {
    val player = Playback.player ?: return
    var playing by remember { mutableStateOf(player.isPlaying) }
    var buffering by remember { mutableStateOf(false) }
    var pos by remember { mutableDoubleStateOf(0.0) }
    var dur by remember { mutableDoubleStateOf(0.0) }
    var dragging by remember { mutableStateOf<Float?>(null) }
    var controls by remember { mutableStateOf(true) }
    var settings by remember { mutableStateOf(false) }
    var touch by remember { mutableIntStateOf(0) }
    val item = Playback.current
    val ctx = androidx.compose.ui.platform.LocalContext.current

    DisposableEffect(player) {
        val l = object : Player.Listener {
            override fun onIsPlayingChanged(isPlaying: Boolean) {
                playing = isPlaying
                (ctx as? PlayerActivity)?.updatePipParams()
            }
            override fun onPlaybackStateChanged(state: Int) { buffering = state == Player.STATE_BUFFERING }
        }
        player.addListener(l)
        onDispose { player.removeListener(l) }
    }
    LaunchedEffect(Unit) {
        while (true) {
            pos = Playback.positionSec()
            dur = Playback.durationSec()
            delay(500)
        }
    }
    LaunchedEffect(controls, playing, touch) {
        if (controls && playing) {
            delay(3500)
            controls = false
        }
    }
    androidx.activity.compose.BackHandler(onBack = onClose)

    Box(Modifier.fillMaxSize().background(Color.Black).clickable(interactionSource = remember { MutableInteractionSource() }, indication = null) { controls = !controls }) {
        AndroidView(factory = { c ->
            PlayerView(c).apply {
                useController = false
                layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                resizeMode = AspectRatioFrameLayout.RESIZE_MODE_FIT
                setShutterBackgroundColor(android.graphics.Color.BLACK)
                keepScreenOn = true
                this.player = player
            }
        }, modifier = Modifier.fillMaxSize())

        if (item?.video == false) Column(Modifier.align(Alignment.Center).padding(32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Icon(Icons.Default.MusicNote, null, Modifier.size(if (inPip) 32.dp else 96.dp), tint = MaterialTheme.colorScheme.primary)
            if (!inPip) Text(item.name, color = Color.White, style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(top = 16.dp))
        }
        Playback.error?.let { err ->
            if (!inPip) Column(Modifier.align(Alignment.Center).background(Color(0xCC000000)).padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Text(stringResource(R.string.a_play_error, err), color = Color.White)
                Button(onClick = { Playback.error = null; Playback.seekTo(pos) }, modifier = Modifier.padding(top = 12.dp)) { Text(stringResource(R.string.retry)) }
            }
        }

        if (!inPip) AnimatedVisibility(controls, enter = fadeIn(), exit = fadeOut(), modifier = Modifier.fillMaxSize()) {
            Box(Modifier.fillMaxSize().background(Color(0x66000000)).safeDrawingPadding()) {
                Row(Modifier.fillMaxWidth().align(Alignment.TopCenter).padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = onClose) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back), tint = Color.White) }
                    Text(item?.name ?: "", color = Color.White, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                    if (item?.video == true) IconButton(onClick = onPip) { Icon(Icons.Default.PictureInPictureAlt, stringResource(R.string.a_pip), tint = Color.White) }
                    IconButton(onClick = { settings = true }) { Icon(Icons.Default.Settings, stringResource(R.string.a_play_settings), tint = Color.White) }
                }
                Row(Modifier.align(Alignment.Center), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(20.dp)) {
                    val many = Playback.queue.size > 1
                    if (many) IconButton(onClick = { touch++; player.seekToPreviousMediaItem() }) { Icon(Icons.Default.SkipPrevious, stringResource(R.string.a_previous), tint = Color.White) }
                    IconButton(onClick = { touch++; Playback.seekTo(pos - 10) }) { Icon(Icons.Default.Replay10, stringResource(R.string.a_back10), tint = Color.White, modifier = Modifier.size(36.dp)) }
                    IconButton(onClick = { touch++; if (playing) player.pause() else player.play() }, modifier = Modifier.size(72.dp)) {
                        Icon(if (playing) Icons.Default.Pause else Icons.Default.PlayArrow, stringResource(if (playing) R.string.a_pause else R.string.a_play), tint = Color.White, modifier = Modifier.size(56.dp))
                    }
                    IconButton(onClick = { touch++; Playback.seekTo(pos + 10) }) { Icon(Icons.Default.Forward10, stringResource(R.string.a_forward10), tint = Color.White, modifier = Modifier.size(36.dp)) }
                    if (many) IconButton(onClick = { touch++; player.seekToNextMediaItem() }) { Icon(Icons.Default.SkipNext, stringResource(R.string.a_next), tint = Color.White) }
                }
                Column(Modifier.fillMaxWidth().align(Alignment.BottomCenter).padding(horizontal = 16.dp, vertical = 8.dp)) {
                    val shown = dragging?.toDouble() ?: pos
                    Slider(
                        value = if (dur > 0) (shown / dur).toFloat().coerceIn(0f, 1f) else 0f,
                        onValueChange = { touch++; dragging = (it * dur).toFloat() },
                        onValueChangeFinished = { dragging?.let { Playback.seekTo(it.toDouble()) }; dragging = null },
                        enabled = dur > 0,
                    )
                    Row {
                        Text(fmt(shown), color = Color.White, style = MaterialTheme.typography.labelMedium)
                        if (buffering) Text("  " + stringResource(R.string.a_buffering), color = Color.White, style = MaterialTheme.typography.labelMedium)
                        Box(Modifier.weight(1f))
                        Text(if (dur > 0) fmt(dur) else "", color = Color.White, style = MaterialTheme.typography.labelMedium)
                    }
                }
            }
        }
    }
    if (settings) PlaySettings(item?.video == true) { settings = false }
}

private fun trackLabel(t: MediaTrack, n: Int): String =
    listOf(t.title, t.lang.uppercase(), t.codec.uppercase(), if (t.channels > 0) "${t.channels}ch" else "").filter { it.isNotBlank() }.distinct()
        .joinToString(" · ").ifEmpty { "#$n" }

@OptIn(UnstableApi::class)
@Composable
private fun PlaySettings(video: Boolean, onClose: () -> Unit) {
    val o = Playback.options
    val probe = Playback.probe
    val player = Playback.player
    var speed by remember { mutableFloatStateOf(player?.playbackParameters?.speed ?: 1f) }
    AlertDialog(
        onDismissRequest = onClose,
        confirmButton = { TextButton(onClick = onClose) { Text(stringResource(R.string.a_close)) } },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                if (video) {
                    Heading(stringResource(R.string.a_quality))
                    Choice(stringResource(R.string.a_quality_auto), !o.original && o.height == 0) { Playback.changeOptions(o.copy(original = false, height = 0)) }
                    listOf(1080, 720, 480, 360).forEach { h -> Choice("${h}p", !o.original && o.height == h) { Playback.changeOptions(o.copy(original = false, height = h)) } }
                    Choice(stringResource(R.string.a_quality_original), o.original) { Playback.changeOptions(o.copy(original = true, height = 0)) }
                    HorizontalDivider(Modifier.padding(vertical = 8.dp))
                }
                if (!o.original) {
                    val audio = probe?.audioTracks.orEmpty()
                    if (audio.size > 1) {
                        Heading(stringResource(R.string.a_audio_track))
                        audio.forEachIndexed { i, t ->
                            val selected = if (o.audio >= 0) o.audio == t.index else probe?.plan?.audio?.index == t.index
                            Choice(trackLabel(t, i + 1), selected) { Playback.changeOptions(o.copy(audio = t.index)) }
                        }
                        HorizontalDivider(Modifier.padding(vertical = 8.dp))
                    }
                    val subs = probe?.subtitles.orEmpty()
                    if (video && subs.isNotEmpty()) {
                        Heading(stringResource(R.string.a_subtitles))
                        Choice(stringResource(R.string.a_subtitles_off), Playback.subtitle == null) { Playback.selectSubtitle(null) }
                        subs.forEachIndexed { i, t -> Choice(trackLabel(t, i + 1), Playback.subtitle == t.index) { Playback.selectSubtitle(t.index) } }
                        HorizontalDivider(Modifier.padding(vertical = 8.dp))
                    }
                } else if (player != null) {
                    // The original file: tracks as the player sees them.
                    val groups = player.currentTracks.groups
                    listOf(C.TRACK_TYPE_AUDIO to R.string.a_audio_track, C.TRACK_TYPE_TEXT to R.string.a_subtitles).forEach { (type, label) ->
                        val gs = groups.filter { it.type == type }
                        if (gs.isEmpty() || (type == C.TRACK_TYPE_AUDIO && gs.sumOf { it.length } < 2)) return@forEach
                        Heading(stringResource(label))
                        if (type == C.TRACK_TYPE_TEXT) Choice(stringResource(R.string.a_subtitles_off), gs.none { it.isSelected }) { Playback.disableText() }
                        gs.forEachIndexed { gi, g ->
                            for (t in 0 until g.length) {
                                val f = g.getTrackFormat(t)
                                val name = listOfNotNull(f.label, f.language?.uppercase(), f.sampleMimeType?.substringAfter('/')).distinct().joinToString(" · ").ifEmpty { "#${gi + 1}" }
                                Choice(name, g.isTrackSelected(t)) { Playback.selectEmbedded(type, gi, t) }
                            }
                        }
                        HorizontalDivider(Modifier.padding(vertical = 8.dp))
                    }
                }
                Heading(stringResource(R.string.a_speed))
                listOf(0.5f, 1f, 1.25f, 1.5f, 2f).forEach { s ->
                    Choice("${s}×", speed == s) { speed = s; player?.setPlaybackSpeed(s) }
                }
            }
        },
    )
}

@Composable
private fun Heading(t: String) = Text(t, style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(vertical = 6.dp))

@Composable
private fun Choice(label: String, selected: Boolean, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().selectable(selected, onClick = onClick).padding(vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        RadioButton(selected = selected, onClick = null)
        Text(label, Modifier.padding(start = 8.dp))
    }
}
