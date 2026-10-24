package io.github.nathplay15.strataos.ui.surveillance

import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Camera
import io.github.nathplay15.strataos.net.CameraReq
import io.github.nathplay15.strataos.net.Nvr
import io.github.nathplay15.strataos.net.addCamera
import io.github.nathplay15.strataos.net.deleteCamera
import io.github.nathplay15.strataos.net.saveCamera
import io.github.nathplay15.strataos.net.testCamera
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Confirm
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.decodeImage
import io.github.nathplay15.strataos.ui.adminkit.rememberActions

/** Stream URLs a camera may use (the NAS checks them again). */
fun cameraUrlProblem(u: String): Boolean {
    val v = u.trim()
    return !(v.startsWith("rtsp://") || v.startsWith("rtsps://") || v.startsWith("http://") || v.startsWith("https://")) || v.any { it == '\'' || it == '"' || it.isWhitespace() }
}

/**
 * Adds or changes a camera: streams and their credentials (the password stays
 * masked unless retyped), recorder, sound, retention, with a connection test.
 */
@Composable
fun CameraEdit(c: Camera?, nvr: Nvr?, onBack: () -> Unit) {
    var name by rememberSaveable { mutableStateOf(c?.name ?: "") }
    var url by rememberSaveable { mutableStateOf(c?.url ?: "rtsp://") }
    var sub by rememberSaveable { mutableStateOf(c?.subUrl ?: "") }
    var user by rememberSaveable { mutableStateOf(c?.username ?: "") }
    var pass by rememberSaveable { mutableStateOf(c?.password ?: "") }
    var enabled by rememberSaveable { mutableStateOf(c?.enabled ?: true) }
    var engine by rememberSaveable { mutableStateOf(c?.engine ?: "") }
    var audio by rememberSaveable { mutableStateOf(c?.audio ?: false) }
    var days by rememberSaveable { mutableStateOf(c?.keepDays?.takeIf { it > 0 }?.toString() ?: "30") }
    var gb by rememberSaveable { mutableStateOf(c?.maxGb?.takeIf { it > 0 }?.let { "%.0f".format(it) } ?: "") }
    var preview by remember { mutableStateOf<ImageBitmap?>(null) }
    var confirmDelete by remember { mutableStateOf(false) }
    var withRecordings by remember { mutableStateOf(false) }
    val actions = rememberActions()
    val req = {
        CameraReq(name = name.trim(), url = url.trim(), subUrl = sub.trim(), username = user, password = pass, enabled = enabled, engine = engine,
            audio = audio, keepDays = days.toIntOrNull() ?: 0, maxGb = gb.replace(',', '.').toDoubleOrNull() ?: 0.0, id = c?.id ?: "")
    }
    val valid = name.isNotBlank() && !cameraUrlProblem(url) && (sub.isBlank() || !cameraUrlProblem(sub))
    val tested = stringResource(R.string.cam_test_ok)

    AreaScreen(if (c == null) stringResource(R.string.cam_add) else c.name, onBack, actions = {
        if (c != null) IconButton(onClick = { confirmDelete = true }) { Icon(Icons.Default.Delete, stringResource(R.string.cam_delete)) }
    }) { pad ->
        Box(Modifier.padding(pad)) {
            ScrollColumn {
                SectionCard(stringResource(R.string.cam_streams)) {
                    Field(stringResource(R.string.cam_name), name, { name = it })
                    Field(stringResource(R.string.cam_main_url), url, { url = it }, error = if (url.isNotBlank() && cameraUrlProblem(url)) stringResource(R.string.cam_url_invalid) else null,
                        help = stringResource(R.string.cam_url_help))
                    Field(stringResource(R.string.cam_sub_url), sub, { sub = it }, error = if (sub.isNotBlank() && cameraUrlProblem(sub)) stringResource(R.string.cam_url_invalid) else null,
                        help = stringResource(R.string.cam_sub_help))
                    Field(stringResource(R.string.cam_username), user, { user = it })
                    Field(stringResource(R.string.cam_password), pass, { pass = it }, secret = true, help = if (c?.password?.isNotEmpty() == true) stringResource(R.string.cam_password_kept) else null)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(enabled = valid && !actions.busy, onClick = { actions.run(done = tested) { preview = decodeImage(it.testCamera(req())) } }) { Text(stringResource(R.string.cam_test)) }
                        if (sub.isNotBlank()) OutlinedButton(enabled = valid && !actions.busy, onClick = {
                            actions.run(done = tested) { preview = decodeImage(it.testCamera(req().copy(stream = "sub"))) }
                        }) { Text(stringResource(R.string.cam_test_sub)) }
                    }
                    preview?.let { Image(it, stringResource(R.string.cam_test_picture), Modifier.fillMaxWidth().aspectRatio(16f / 9f), contentScale = ContentScale.Fit) }
                }
                SectionCard(stringResource(R.string.cam_recording)) {
                    SwitchLine(stringResource(R.string.cam_enabled), enabled) { enabled = it }
                    Choice(stringResource(R.string.cam_engine), listOf("" to stringResource(R.string.cam_engine_builtin), "frigate" to stringResource(R.string.cam_engine_frigate)), engine, { engine = it })
                    Text(stringResource(if (engine == "frigate") R.string.cam_engine_frigate_help else R.string.cam_engine_builtin_help), style = MaterialTheme.typography.bodySmall)
                    if (engine == "frigate" && nvr?.frigate?.installed != true) Text(stringResource(R.string.cam_frigate_missing), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
                    if (engine != "frigate") SwitchLine(stringResource(R.string.cam_audio), audio, sub = stringResource(R.string.cam_audio_help)) { audio = it }
                }
                SectionCard(stringResource(R.string.cam_retention)) {
                    Field(stringResource(R.string.cam_keep_days), days, { days = it.filter(Char::isDigit) }, number = true, help = stringResource(R.string.cam_zero_no_limit))
                    if (engine != "frigate") Field(stringResource(R.string.cam_max_gb), gb, { gb = it.filter { ch -> ch.isDigit() || ch == '.' || ch == ',' } }, number = true, help = stringResource(R.string.cam_zero_no_limit))
                }
                Button(onClick = {
                    actions.run(after = onBack) { if (c == null) it.addCamera(req()) else it.saveCamera(c.id, req()) }
                }, enabled = valid && !actions.busy, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.b_save)) }
            }
        }
    }
    if (confirmDelete && c != null) Confirm(
        title = stringResource(R.string.cam_delete), text = stringResource(R.string.cam_delete_text, c.name), confirm = stringResource(R.string.b_delete),
        typeToConfirm = if (withRecordings) c.name else null,
        onConfirm = { actions.run(after = onBack) { it.deleteCamera(c.id, withRecordings) } }, onDismiss = { confirmDelete = false },
        extra = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Checkbox(withRecordings, { withRecordings = it })
                Text(stringResource(R.string.cam_delete_recordings))
            }
        },
    )
}
