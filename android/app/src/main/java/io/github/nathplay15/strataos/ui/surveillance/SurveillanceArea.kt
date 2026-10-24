package io.github.nathplay15.strataos.ui.surveillance

import androidx.compose.foundation.Image
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
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Videocam
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Camera
import io.github.nathplay15.strataos.net.NasJob
import io.github.nathplay15.strataos.net.Nvr
import io.github.nathplay15.strataos.net.installComponent
import io.github.nathplay15.strataos.net.livePicture
import io.github.nathplay15.strataos.net.nvr
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Data
import io.github.nathplay15.strataos.ui.adminkit.Empty
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.decodeImage
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext

// Surveillance: camera grid (live pictures), one camera in live video,
// recordings with playback, Frigate events, camera and global settings.
// Screens are routes kept across rotation: "grid", "live:<id>", "rec:<id>",
// "events:<id>", "edit:<id>" ("edit:" adds a camera), "settings".

@Composable
fun SurveillanceArea(back: () -> Unit) {
    var route by rememberSaveable { mutableStateOf("grid") }
    val data = rememberData(Unit) { it.nvr() }
    val go: (String) -> Unit = { route = it }
    val home = { route = "grid"; data.reload() }
    val (kind, id) = route.split(":", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
    val cam = data.value?.cameras?.firstOrNull { it.id == id }
    when {
        kind == "settings" -> SurveillanceSettings(onBack = home)
        kind == "edit" && (id.isEmpty() || cam != null) -> CameraEdit(cam, data.value, onBack = home)
        kind == "live" && cam != null -> CameraLive(cam, onBack = home, open = go)
        kind == "rec" && cam != null -> CameraRecordings(cam, onBack = { route = "live:${cam.id}" })
        kind == "events" && cam != null -> CameraEvents(cam, onBack = { route = "live:${cam.id}" })
        else -> CameraGrid(data, back, go)
    }
}

/** State of a camera as a badge. */
@Composable
fun CameraState(c: Camera) {
    val s = c.status.state
    val (label, tone) = when (s) {
        "recording" -> R.string.cam_state_recording to Tone.Ok
        "frigate" -> R.string.cam_state_frigate to Tone.Accent
        "disabled" -> R.string.cam_state_disabled to Tone.Neutral
        "offline" -> R.string.cam_state_offline to Tone.Bad
        "connecting" -> R.string.cam_state_connecting to Tone.Warn
        else -> R.string.cam_state_stopped to Tone.Neutral
    }
    StatusBadge(stringResource(label), tone)
}

@Composable
private fun CameraGrid(data: Data<Nvr>, back: () -> Unit, go: (String) -> Unit) {
    val actions = rememberActions()
    var job by remember { mutableStateOf<NasJob?>(null) }
    AreaScreen(stringResource(R.string.area_surveillance), back, actions = {
        IconButton(onClick = { go("edit:") }) { Icon(Icons.Default.Add, stringResource(R.string.cam_add)) }
        IconButton(onClick = { go("settings") }) { Icon(Icons.Default.Settings, stringResource(R.string.cam_settings)) }
    }) { pad ->
        Refreshable(data, pad) { n ->
            when {
                !n.installed -> Empty(Icons.Default.Videocam, stringResource(R.string.cam_not_installed)) {
                    Button(onClick = { actions.run { job = it.installComponent("nvr") } }, enabled = !actions.busy) { Text(stringResource(R.string.cam_install)) }
                }
                n.cameras.isEmpty() -> Empty(Icons.Default.Videocam, stringResource(if (n.folder.isEmpty()) R.string.cam_none_folder else R.string.cam_none)) {
                    Button(onClick = { go(if (n.folder.isEmpty()) "settings" else "edit:") }) {
                        Text(stringResource(if (n.folder.isEmpty()) R.string.cam_choose_folder else R.string.cam_add))
                    }
                }
                else -> LazyVerticalGrid(GridCells.Adaptive(300.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(12.dp),
                    horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    if (n.folderError.isNotEmpty() || n.folder.isEmpty()) item(span = { GridItemSpan(maxLineSpan) }) {
                        Card(Modifier.fillMaxWidth().clickable { go("settings") }) {
                            Text(n.folderError.ifEmpty { stringResource(R.string.cam_none_folder) }, Modifier.padding(16.dp), color = MaterialTheme.colorScheme.error)
                        }
                    }
                    items(n.cameras, key = { it.id }) { c -> CameraTile(c, go) }
                }
            }
        }
    }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

/** A camera of the grid: its live picture (about one every two seconds), name and state. */
@Composable
private fun CameraTile(c: Camera, go: (String) -> Unit) {
    var pic by remember(c.id) { mutableStateOf<ImageBitmap?>(null) }
    var failed by remember(c.id) { mutableStateOf(false) }
    var menu by remember { mutableStateOf(false) }
    if (c.enabled) LaunchedEffect(c.id) {
        while (true) {
            val img = runCatching { withContext(Dispatchers.IO) { decodeImage(api().livePicture(c.id), 960) } }.getOrNull()
            if (img != null) pic = img
            failed = img == null
            delay(if (img != null) 2000 else 5000)
        }
    }
    Card(Modifier.fillMaxWidth().clickable { go("live:${c.id}") }) {
        Box(Modifier.fillMaxWidth().aspectRatio(16f / 9f).background(Color.Black), contentAlignment = Alignment.Center) {
            val p = pic
            when {
                p != null -> Image(p, stringResource(R.string.cam_picture_of, c.name), Modifier.fillMaxSize(), contentScale = ContentScale.Fit)
                !c.enabled -> Text(stringResource(R.string.cam_state_disabled), color = Color.White.copy(alpha = 0.7f))
                failed -> Text(c.status.error.ifEmpty { stringResource(R.string.cam_no_picture) }, Modifier.padding(16.dp), color = Color.White.copy(alpha = 0.8f),
                    style = MaterialTheme.typography.bodySmall, maxLines = 3, overflow = TextOverflow.Ellipsis)
                else -> CircularProgressIndicator()
            }
            if (c.status.state == "recording") Row(Modifier.align(Alignment.TopStart).padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(10.dp).background(Color.Red, CircleShape))
                Text(" REC", color = Color.White, style = MaterialTheme.typography.labelSmall)
            }
        }
        Row(Modifier.fillMaxWidth().padding(start = 12.dp, top = 4.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(c.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(stringResource(R.string.cam_tile_info, c.segments, formatBytes(c.size)), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            CameraState(c)
            Box {
                IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.b_more)) }
                DropdownMenu(menu, { menu = false }) {
                    DropdownMenuItem(text = { Text(stringResource(R.string.cam_recordings)) }, onClick = { menu = false; go("rec:${c.id}") })
                    if (c.frigate) DropdownMenuItem(text = { Text(stringResource(R.string.cam_events)) }, onClick = { menu = false; go("events:${c.id}") })
                    DropdownMenuItem(text = { Text(stringResource(R.string.cam_edit)) }, onClick = { menu = false; go("edit:${c.id}") })
                }
            }
        }
    }
}
