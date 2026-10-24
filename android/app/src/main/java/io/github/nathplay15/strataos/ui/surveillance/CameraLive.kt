package io.github.nathplay15.strataos.ui.surveillance

import android.content.res.Configuration
import androidx.activity.compose.BackHandler
import androidx.activity.compose.LocalActivity
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Event
import androidx.compose.material.icons.filled.Fullscreen
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.VideoLibrary
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Camera
import io.github.nathplay15.strataos.net.liveVideoUrl
import io.github.nathplay15.strataos.net.livePicture
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.decodeImage
import io.github.nathplay15.strataos.ui.adminkit.readable
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext

/**
 * One camera live: video remuxed by the NAS (main stream, or the sub-stream
 * for a slow connection), pictures for cameras without live video or whose
 * codec the phone cannot decode. In landscape the video fills the screen.
 */
@Composable
fun CameraLive(c: Camera, onBack: () -> Unit, open: (String) -> Unit) {
    var hd by rememberSaveable(c.id) { mutableStateOf(true) }
    var pictures by rememberSaveable(c.id) { mutableStateOf(!c.liveVideo) }
    var error by remember(c.id) { mutableStateOf<String?>(null) }
    var restart by remember { mutableIntStateOf(0) }
    var forceFull by rememberSaveable { mutableStateOf(false) }
    // A phone turned sideways shows the video alone; a tablet keeps the page.
    val conf = LocalConfiguration.current
    val full = forceFull || (conf.orientation == Configuration.ORIENTATION_LANDSCAPE && conf.screenHeightDp < 600)

    val view: @Composable BoxScope.() -> Unit = {
        if (!c.enabled) Text(stringResource(R.string.cam_state_disabled), Modifier.align(Alignment.Center), color = Color.White)
        else if (pictures) LivePictures(c, Modifier.fillMaxSize())
        else androidx.compose.runtime.key(hd, restart) {
            NasVideo(api().liveVideoUrl(c.id, hd || c.subUrl.isEmpty()), live = true, modifier = Modifier.fillMaxSize(),
                onError = { e ->
                    // A codec the phone cannot play, or a camera the NAS cannot stream: pictures instead.
                    error = e.cause?.message ?: e.errorCodeName
                    pictures = true
                })
        }
    }

    if (full) {
        BackHandler { if (forceFull) forceFull = false else onBack() }
        Immersive()
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            view()
            IconButton(onClick = { if (forceFull) forceFull = false else onBack() }, Modifier.align(Alignment.TopStart).padding(8.dp),
                colors = IconButtonDefaults.iconButtonColors(containerColor = Color.Black.copy(alpha = 0.4f), contentColor = Color.White)) {
                Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.b_back))
            }
        }
        return
    }

    AreaScreen(c.name, onBack, actions = {
        IconButton(onClick = { error = null; pictures = !c.liveVideo; restart++ }) { Icon(Icons.Default.Refresh, stringResource(R.string.cam_reconnect)) }
        IconButton(onClick = { forceFull = true }) { Icon(Icons.Default.Fullscreen, stringResource(R.string.cam_fullscreen)) }
    }) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState())) {
            Box(Modifier.fillMaxWidth().aspectRatio(16f / 9f).background(Color.Black), content = view)
            Column(Modifier.readable().padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    CameraState(c)
                    if (c.liveVideo) {
                        FilterChip(selected = !pictures, onClick = { pictures = false; error = null; restart++ }, label = { Text(stringResource(R.string.cam_video)) })
                        FilterChip(selected = pictures, onClick = { pictures = true }, label = { Text(stringResource(R.string.cam_pictures)) })
                    }
                    if (!pictures && c.subUrl.isNotEmpty()) FilterChip(selected = hd, onClick = { hd = !hd }, label = { Text(stringResource(R.string.cam_hd)) })
                }
                error?.let { Text(stringResource(R.string.cam_video_failed, it), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error) }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(onClick = { open("rec:${c.id}") }) { Icon(Icons.Default.VideoLibrary, null); Text(" " + stringResource(R.string.cam_recordings)) }
                    if (c.frigate) OutlinedButton(onClick = { open("events:${c.id}") }) { Icon(Icons.Default.Event, null); Text(" " + stringResource(R.string.cam_events)) }
                    OutlinedButton(onClick = { open("edit:${c.id}") }) { Icon(Icons.Default.Edit, null); Text(" " + stringResource(R.string.cam_edit)) }
                }
                SectionCard {
                    if (c.status.error.isNotEmpty()) KeyValue(stringResource(R.string.cam_last_error), c.status.error)
                    if (c.status.since.isNotEmpty()) KeyValue(stringResource(R.string.cam_since), formatDate(c.status.since))
                    KeyValue(stringResource(R.string.cam_recorded), stringResource(R.string.cam_tile_info, c.segments, formatBytes(c.size)))
                    c.oldest?.let { KeyValue(stringResource(R.string.cam_oldest), formatDate(it)) }
                    KeyValue(stringResource(R.string.cam_retention), retentionText(c))
                }
            }
        }
    }
}

@Composable
fun retentionText(c: Camera): String {
    val parts = mutableListOf<String>()
    if (c.keepDays > 0) parts += stringResource(R.string.cam_days, c.keepDays)
    if (c.maxGb > 0 && !c.frigate) parts += "%.0f GB".format(c.maxGb)
    return parts.joinToString(" · ").ifEmpty { stringResource(R.string.cam_no_limit) }
}

/** The live picture of a camera, refreshed about once a second. */
@Composable
fun LivePictures(c: Camera, modifier: Modifier) {
    var pic by remember(c.id) { mutableStateOf<ImageBitmap?>(null) }
    var failed by remember(c.id) { mutableStateOf(false) }
    LaunchedEffect(c.id) {
        while (true) {
            val img = runCatching { withContext(Dispatchers.IO) { decodeImage(api().livePicture(c.id), 1920) } }.getOrNull()
            if (img != null) pic = img
            failed = img == null && pic == null
            delay(if (img != null) 1000 else 4000)
        }
    }
    Box(modifier, contentAlignment = Alignment.Center) {
        val p = pic
        when {
            p != null -> Image(p, stringResource(R.string.cam_picture_of, c.name), Modifier.fillMaxSize(), contentScale = ContentScale.Fit)
            failed -> Text(stringResource(R.string.cam_no_picture), color = Color.White)
            else -> CircularProgressIndicator()
        }
    }
}

/** Hides the system bars while shown (full-screen video). */
@Composable
fun Immersive() {
    val activity = LocalActivity.current ?: return
    DisposableEffect(activity) {
        val ctl = WindowCompat.getInsetsController(activity.window, activity.window.decorView)
        ctl.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        ctl.hide(WindowInsetsCompat.Type.systemBars())
        onDispose { ctl.show(WindowInsetsCompat.Type.systemBars()) }
    }
}
