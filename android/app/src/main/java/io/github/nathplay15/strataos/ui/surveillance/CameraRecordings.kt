package io.github.nathplay15.strataos.ui.surveillance

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ContentCut
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.VideoLibrary
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Camera
import io.github.nathplay15.strataos.net.Segment
import io.github.nathplay15.strataos.net.SegmentInfo
import io.github.nathplay15.strataos.net.clipUrl
import io.github.nathplay15.strataos.net.recordings
import io.github.nathplay15.strataos.net.segmentInfo
import io.github.nathplay15.strataos.net.segmentPlayUrl
import io.github.nathplay15.strataos.net.segmentUrl
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.downloadAndShare
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.adminkit.rememberSaveAs
import io.github.nathplay15.strataos.ui.adminkit.saveUrlTo
import io.github.nathplay15.strataos.ui.formatBytes
import java.text.DateFormat
import java.time.LocalDate
import java.time.ZoneId
import java.util.Date
import okhttp3.HttpUrl

private fun timeOf(iso: String): String = isoMillis(iso)?.let { DateFormat.getTimeInstance(DateFormat.MEDIUM).format(Date(it)) } ?: ""
private fun dayLabel(day: String): String = runCatching {
    DateFormat.getDateInstance(DateFormat.MEDIUM).format(Date.from(LocalDate.parse(day).atStartOfDay(ZoneId.systemDefault()).toInstant()))
}.getOrDefault(day)

/** The recordings of a camera: one day at a time, its timeline, playback and clips. */
@Composable
fun CameraRecordings(c: Camera, onBack: () -> Unit) {
    var day by rememberSaveable(c.id) { mutableStateOf<String?>(null) }
    val data = rememberData(c.id, day) { it.recordings(c.id, day) }
    var playing by rememberSaveable(c.id) { mutableStateOf<String?>(null) }
    var startSec by rememberSaveable(c.id) { mutableIntStateOf(0) }
    var position by remember { mutableLongStateOf(0L) }
    var clipOf by remember { mutableStateOf<Segment?>(null) }
    val ctx = LocalContext.current
    val actions = rememberActions()
    var pendingSave by remember { mutableStateOf<HttpUrl?>(null) }
    val saveAs = rememberSaveAs("video/mp4") { uri -> pendingSave?.let { saveUrlTo(ctx, it, uri) } }
    val save: (HttpUrl, String) -> Unit = { u, name -> pendingSave = u; saveAs(name) }
    val share: (HttpUrl, String) -> Unit = { u, name -> actions.run { downloadAndShare(ctx, u, name, "video/mp4") } }

    AreaScreen(c.name, onBack, subtitle = stringResource(R.string.cam_recordings)) { pad ->
        Refreshable(data, pad) { rec ->
            val seg = rec.segments.firstOrNull { it.name == playing }
            LazyColumn(Modifier.fillMaxSize()) {
                item {
                    Box(Modifier.fillMaxWidth().aspectRatio(16f / 9f).background(Color.Black), contentAlignment = Alignment.Center) {
                        if (seg != null) SegmentPlayer(c, seg, startSec) { position = it }
                        else Text(stringResource(R.string.cam_pick_time), Modifier.padding(16.dp), color = Color.White.copy(alpha = 0.8f))
                    }
                }
                item {
                    LazyRow(Modifier.fillMaxWidth().padding(vertical = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 12.dp)) {
                        items(rec.days) { d -> FilterChip(selected = d == rec.day, onClick = { day = d; playing = null }, label = { Text(dayLabel(d)) }) }
                    }
                }
                item {
                    DayTimeline(rec.day, rec.segments, seg?.name) { s, off -> playing = s.name; startSec = off }
                }
                if (rec.segments.isEmpty()) item { Box(Modifier.height(240.dp)) { Empty(Icons.Default.VideoLibrary, stringResource(R.string.cam_no_recordings)) } }
                items(rec.segments.asReversed(), key = { it.name }) { s ->
                    SegmentRow(s, s.name == playing,
                        play = { playing = s.name; startSec = 0 },
                        save = { save(api().segmentUrl(c.id, s.name).newBuilder().addQueryParameter("download", "1").build(), "${c.name} ${s.name}") },
                        share = { share(api().segmentUrl(c.id, s.name), "${c.name} ${s.name}") },
                        clip = { clipOf = s })
                    HorizontalDivider()
                }
            }
        }
    }
    clipOf?.let { s ->
        val from = if (s.name == playing) ((position / 1000).toInt()) else 0
        ClipDialog(s, from, onDismiss = { clipOf = null }) { a, b, toShare ->
            clipOf = null
            val u = api().clipUrl(c.id, s.name, a, b)
            val name = "${c.name} ${s.name.substringBeforeLast('.')} +${a}s.mp4"
            if (toShare) share(u, name) else save(u, name)
        }
    }
}

/** The 24 hours of a day with its recordings; a tap plays from that time. */
@Composable
private fun DayTimeline(day: String, segments: List<Segment>, selected: String?, pick: (Segment, Int) -> Unit) {
    val start = dayStart(day) ?: return
    val sp = remember(segments, day) { spans(segments, start) }
    val accent = MaterialTheme.colorScheme.primary
    val sel = MaterialTheme.colorScheme.tertiary
    val track = MaterialTheme.colorScheme.surfaceVariant
    val grid = MaterialTheme.colorScheme.outline
    val desc = stringResource(R.string.cam_timeline_desc, segments.size)
    Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp)) {
        Canvas(Modifier.fillMaxWidth().height(44.dp).semantics { contentDescription = desc }.pointerInput(sp) {
            detectTapGestures { o -> segmentAt(sp, start, o.x / size.width)?.let { (s, off) -> pick(s, off) } }
        }) {
            drawRect(track, size = Size(size.width, size.height))
            sp.forEach { s ->
                drawRect(if (s.segment.name == selected) sel else accent, topLeft = Offset(s.from * size.width, 0f), size = Size(((s.to - s.from) * size.width).coerceAtLeast(2f), size.height))
            }
            for (h in 1 until 24) {
                val x = size.width * h / 24f
                drawLine(grid, Offset(x, size.height * (if (h % 6 == 0) 0.55f else 0.8f)), Offset(x, size.height), strokeWidth = 1f)
            }
        }
        Row(Modifier.fillMaxWidth()) {
            listOf("0", "6", "12", "18", "24").forEachIndexed { i, t ->
                Text(t, Modifier.weight(1f), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = when (i) { 0 -> androidx.compose.ui.text.style.TextAlign.Start; 4 -> androidx.compose.ui.text.style.TextAlign.End; else -> androidx.compose.ui.text.style.TextAlign.Center })
            }
        }
    }
}

@Composable
private fun SegmentRow(s: Segment, current: Boolean, play: () -> Unit, save: () -> Unit, share: () -> Unit, clip: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    ListItem(
        modifier = Modifier.clickable(enabled = !s.open, onClick = play),
        headlineContent = { Text("${timeOf(s.start)} – ${timeOf(s.end)}") },
        supportingContent = { Text(formatBytes(s.size)) },
        leadingContent = { Icon(Icons.Default.PlayArrow, null, tint = if (current) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant) },
        trailingContent = {
            if (s.open) StatusBadge(stringResource(R.string.cam_being_recorded), Tone.Warn)
            else Box {
                IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.b_more)) }
                DropdownMenu(menu, { menu = false }) {
                    DropdownMenuItem(text = { Text(stringResource(R.string.b_save)) }, leadingIcon = { Icon(Icons.Default.Download, null) }, onClick = { menu = false; save() })
                    DropdownMenuItem(text = { Text(stringResource(R.string.b_share)) }, leadingIcon = { Icon(Icons.Default.Share, null) }, onClick = { menu = false; share() })
                    DropdownMenuItem(text = { Text(stringResource(R.string.cam_clip)) }, leadingIcon = { Icon(Icons.Default.ContentCut, null) }, onClick = { menu = false; clip() })
                }
            }
        },
    )
}

/**
 * Plays a segment: the file itself when the phone can (seeking in the
 * player), else the NAS repackages or converts it from the chosen second.
 */
@Composable
private fun SegmentPlayer(c: Camera, s: Segment, startSec: Int, position: (Long) -> Unit) {
    val info = rememberData(c.id, s.name) { it.segmentInfo(c.id, s.name, decodesHevc) }
    val i: SegmentInfo? = info.value
    var from by remember(s.name, startSec) { mutableIntStateOf(startSec) }
    when {
        info.state is io.github.nathplay15.strataos.ui.Loaded.Failed -> Text(stringResource(R.string.cam_unplayable), color = Color.White)
        i == null -> CircularProgressIndicator()
        i.direct -> androidx.compose.runtime.key(s.name, startSec) {
            NasVideo(api().segmentUrl(c.id, s.name), live = false, modifier = Modifier.fillMaxSize(), startMs = startSec * 1000L, position = position)
        }
        else -> Column(Modifier.fillMaxSize()) {
            androidx.compose.runtime.key(s.name, from) {
                NasVideo(api().segmentPlayUrl(c.id, s.name, from, decodesHevc), live = false, modifier = Modifier.weight(1f).fillMaxWidth(),
                    position = { position(from * 1000L + it) })
            }
            var slide by remember(s.name, from) { mutableFloatStateOf(from.toFloat()) }
            val dur = i.duration.toInt().coerceAtLeast(1)
            Row(Modifier.fillMaxWidth().background(Color.Black.copy(alpha = 0.6f)).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Slider(slide, { slide = it }, Modifier.weight(1f), valueRange = 0f..dur.toFloat(), onValueChangeFinished = { from = slide.toInt() })
                Text(clock(slide.toInt()), color = Color.White, style = MaterialTheme.typography.labelMedium)
            }
        }
    }
}

/** Choose the part of a segment to export (stream copy on the NAS). */
@Composable
private fun ClipDialog(s: Segment, from: Int, onDismiss: () -> Unit, go: (Int, Int, Boolean) -> Unit) {
    val total = ((isoMillis(s.end) ?: 0) - (isoMillis(s.start) ?: 0)).div(1000).toInt().coerceAtLeast(1)
    var start by remember { mutableFloatStateOf(from.coerceIn(0, total - 1).toFloat()) }
    var length by remember { mutableIntStateOf(60) }
    val end = (start.toInt() + length).coerceAtMost(total)
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.cam_clip)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(stringResource(R.string.cam_clip_from, clock(start.toInt())))
                Slider(start, { start = it }, valueRange = 0f..(total - 1).toFloat())
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    listOf(30, 60, 300, 600).forEach { l -> FilterChip(selected = length == l, onClick = { length = l }, label = { Text(clock(l)) }) }
                }
                Text(stringResource(R.string.cam_clip_range, clock(start.toInt()), clock(end)), style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = {
            Row {
                TextButton(onClick = { go(start.toInt(), end, true) }) { Text(stringResource(R.string.b_share)) }
                TextButton(onClick = { go(start.toInt(), end, false) }) { Text(stringResource(R.string.b_save)) }
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.b_cancel)) } },
    )
}
