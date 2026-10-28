package io.github.nathplay15.strataos.ui.surveillance

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.Event
import androidx.compose.material.icons.filled.Share
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.Camera
import io.github.nathplay15.strataos.net.manage.FrigateEvent
import io.github.nathplay15.strataos.net.manage.frigateEvents
import io.github.nathplay15.strataos.net.manage.frigateFileUrl
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.AuthImage
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.downloadAndShare
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.adminkit.rememberSaveAs
import io.github.nathplay15.strataos.ui.adminkit.saveUrlTo
import java.text.DateFormat
import java.time.LocalDate
import java.time.ZoneId
import java.util.Date

/** Labels of the events kept by [filter] ("" = all). */
fun filterEvents(events: List<FrigateEvent>, filter: String): List<FrigateEvent> =
    if (filter.isEmpty()) events else events.filter { it.label == filter }

/** What Frigate detected on a camera, one day at a time: snapshots, clips, filters by object. */
@Composable
fun CameraEvents(c: Camera, onBack: () -> Unit) {
    val today = remember { LocalDate.now() }
    var day by rememberSaveable(c.id) { mutableStateOf(today.toString()) }
    var label by rememberSaveable(c.id) { mutableStateOf("") }
    var playing by rememberSaveable(c.id) { mutableStateOf<String?>(null) }
    val data = rememberData(c.id, day) { it.frigateEvents(c.id, day) }
    val ctx = LocalContext.current
    val actions = rememberActions()
    var saving by remember { mutableStateOf<FrigateEvent?>(null) }
    val saveAs = rememberSaveAs("video/mp4") { uri -> saving?.let { saveUrlTo(ctx, api().frigateFileUrl(c.id, it.id, "clip.mp4"), uri) } }
    val clipName: (FrigateEvent) -> String = { e -> "${c.name} ${e.start.take(19).replace(":", "")} ${e.label}.mp4" }

    AreaScreen(c.name, onBack, subtitle = stringResource(R.string.cam_events)) { pad ->
        Refreshable(data, pad) { ev ->
            val labels = ev.events.map { it.label }.distinct().sorted()
            val shown = filterEvents(ev.events, label).sortedByDescending { it.start }
            LazyColumn(Modifier.fillMaxSize()) {
                shown.firstOrNull { it.id == playing }?.let { e ->
                    item {
                        Box(Modifier.fillMaxWidth().aspectRatio(16f / 9f).background(Color.Black)) {
                            androidx.compose.runtime.key(e.id) { NasVideo(api().frigateFileUrl(c.id, e.id, "clip.mp4"), live = false, modifier = Modifier.fillMaxSize()) }
                        }
                    }
                }
                item {
                    LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp), contentPadding = PaddingValues(12.dp)) {
                        items((0..6).map { today.minusDays(it.toLong()).toString() }) { d ->
                            FilterChip(selected = d == day, onClick = { day = d; playing = null }, label = { Text(dayName(d, today)) })
                        }
                    }
                }
                if (labels.size > 1) item {
                    LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp), contentPadding = PaddingValues(horizontal = 12.dp)) {
                        item { FilterChip(selected = label.isEmpty(), onClick = { label = "" }, label = { Text(stringResource(R.string.cam_all_objects)) }) }
                        items(labels) { l -> FilterChip(selected = label == l, onClick = { label = l }, label = { Text(objectName(l)) }) }
                    }
                }
                if (ev.error.isNotEmpty()) item { Text(ev.error, Modifier.padding(16.dp), color = MaterialTheme.colorScheme.error) }
                if (!ev.installed) item { Text(stringResource(R.string.cam_frigate_missing), Modifier.padding(16.dp)) }
                if (shown.isEmpty()) item { Box(Modifier.height(260.dp)) { Empty(Icons.Default.Event, stringResource(R.string.cam_no_events)) } }
                items(shown, key = { it.id }) { e ->
                    ListItem(
                        modifier = Modifier.clickable { playing = e.id },
                        leadingContent = {
                            AuthImage(api().frigateFileUrl(c.id, e.id, "thumbnail.jpg"), stringResource(R.string.cam_snapshot_of, objectName(e.label)),
                                Modifier.size(96.dp, 54.dp))
                        },
                        headlineContent = { Text("${objectName(e.label)} · ${(e.score * 100).toInt()} %") },
                        supportingContent = { Text(timeRange(e)) },
                        trailingContent = {
                            Row {
                                IconButton(onClick = { actions.run { downloadAndShare(ctx, it.frigateFileUrl(c.id, e.id, "clip.mp4"), clipName(e), "video/mp4") } }) {
                                    Icon(Icons.Default.Share, stringResource(R.string.b_share))
                                }
                                IconButton(onClick = { saving = e; saveAs(clipName(e)) }) { Icon(Icons.Default.Download, stringResource(R.string.b_save)) }
                            }
                        },
                    )
                    HorizontalDivider()
                }
            }
        }
    }
}

private fun timeRange(e: FrigateEvent): String {
    val f = DateFormat.getTimeInstance(DateFormat.SHORT)
    val a = isoMillis(e.start)?.let { f.format(Date(it)) } ?: ""
    val b = e.end?.let { isoMillis(it) }?.let { f.format(Date(it)) }
    return if (b != null) "$a – $b" else a
}

@Composable
private fun dayName(d: String, today: LocalDate): String = when (d) {
    today.toString() -> stringResource(R.string.cam_today)
    today.minusDays(1).toString() -> stringResource(R.string.cam_yesterday)
    else -> runCatching { DateFormat.getDateInstance(DateFormat.MEDIUM).format(Date.from(LocalDate.parse(d).atStartOfDay(ZoneId.systemDefault()).toInstant())) }.getOrDefault(d)
}

/** The objects Frigate tracks, in the language of the phone. */
@Composable
fun objectName(o: String): String = when (o) {
    "person" -> stringResource(R.string.obj_person)
    "car" -> stringResource(R.string.obj_car)
    "bicycle" -> stringResource(R.string.obj_bicycle)
    "motorcycle" -> stringResource(R.string.obj_motorcycle)
    "bus" -> stringResource(R.string.obj_bus)
    "truck" -> stringResource(R.string.obj_truck)
    "dog" -> stringResource(R.string.obj_dog)
    "cat" -> stringResource(R.string.obj_cat)
    "bird" -> stringResource(R.string.obj_bird)
    "horse" -> stringResource(R.string.obj_horse)
    else -> o
}
