package io.github.nathplay15.strataos.ui.photos

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.calculatePan
import androidx.compose.foundation.gestures.calculateZoom
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.PlayCircle
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.positionChanged
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.NasImage
import okhttp3.HttpUrl

/** A picture (or a video, shown by its thumbnail) of a full-screen pager. */
data class PagerImage(val path: String, val name: String, val url: HttpUrl, val video: Boolean = false, val subtitle: String = "", val alternatives: List<HttpUrl> = emptyList())

/**
 * Full-screen viewer: swipe between pictures, pinch or double tap to zoom,
 * tap to show or hide the bar. [actions] are the buttons of the bar for the
 * picture shown; [onPlay] opens a video.
 */
@Composable
fun ImagePager(
    items: List<PagerImage>,
    start: Int,
    onClose: () -> Unit,
    onPlay: (PagerImage) -> Unit = {},
    actions: @Composable RowScope.(PagerImage) -> Unit = {},
) {
    BackHandler(onBack = onClose)
    if (items.isEmpty()) {
        onClose()
        return
    }
    val pager = rememberPagerState(initialPage = start.coerceIn(0, items.size - 1)) { items.size }
    var chrome by remember { mutableStateOf(true) }
    Box(Modifier.fillMaxSize().background(Color.Black)) {
        HorizontalPager(pager, Modifier.fillMaxSize(), beyondViewportPageCount = 1, key = { items[it].path }) { i ->
            val it = items[i]
            if (it.video) {
                Box(Modifier.fillMaxSize().pointerInput(it) { detectTapGestures(onTap = { _ -> onPlay(it) }) }, contentAlignment = Alignment.Center) {
                    NasImage(it.url, it.name, Modifier.fillMaxSize(), maxPx = 1600, contentScale = ContentScale.Fit)
                    Icon(Icons.Default.PlayCircle, stringResource(R.string.a_play), Modifier.size(80.dp), tint = Color.White)
                }
            } else {
                Zoomable(onTap = { chrome = !chrome }) {
                    NasImage(it.url, it.name, Modifier.fillMaxSize(), maxPx = 2048, contentScale = ContentScale.Fit, alternatives = it.alternatives)
                }
            }
        }
        if (chrome) {
            val cur = items[pager.currentPage.coerceIn(0, items.size - 1)]
            Row(Modifier.fillMaxWidth().background(Color(0x88000000)).safeDrawingPadding().padding(4.dp), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onClose) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back), tint = Color.White) }
                androidx.compose.foundation.layout.Column(Modifier.weight(1f)) {
                    Text(cur.name, color = Color.White, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (cur.subtitle.isNotEmpty()) Text(cur.subtitle, color = Color.White.copy(alpha = 0.7f), style = MaterialTheme.typography.bodySmall, maxLines = 1)
                }
                androidx.compose.runtime.CompositionLocalProvider(androidx.compose.material3.LocalContentColor provides Color.White) { actions(cur) }
            }
        }
    }
}

/** Pinch and double tap zoom that leaves one-finger swipes to the pager while not zoomed. */
@Composable
private fun Zoomable(onTap: () -> Unit, content: @Composable () -> Unit) {
    var scale by remember { mutableFloatStateOf(1f) }
    var offset by remember { mutableStateOf(Offset.Zero) }
    Box(
        Modifier.fillMaxSize()
            .pointerInput(Unit) {
                awaitEachGesture {
                    awaitFirstDown(requireUnconsumed = false)
                    do {
                        val event = awaitPointerEvent()
                        if (event.changes.size > 1 || scale > 1f) {
                            scale = (scale * event.calculateZoom()).coerceIn(1f, 6f)
                            val maxX = size.width * (scale - 1) / 2
                            val maxY = size.height * (scale - 1) / 2
                            val o = offset + event.calculatePan()
                            offset = if (scale == 1f) Offset.Zero else Offset(o.x.coerceIn(-maxX, maxX), o.y.coerceIn(-maxY, maxY))
                            event.changes.forEach { if (it.positionChanged()) it.consume() }
                        }
                    } while (event.changes.any { it.pressed })
                }
            }
            .pointerInput(Unit) {
                detectTapGestures(onTap = { onTap() }, onDoubleTap = {
                    if (scale > 1f) { scale = 1f; offset = Offset.Zero } else scale = 2.5f
                })
            }
            .graphicsLayer(scaleX = scale, scaleY = scale, translationX = offset.x, translationY = offset.y),
    ) { content() }
}
