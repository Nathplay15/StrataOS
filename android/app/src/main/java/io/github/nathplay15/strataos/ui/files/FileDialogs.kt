package io.github.nathplay15.strataos.ui.files

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.data.favorites
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.net.NewLink
import io.github.nathplay15.strataos.net.ShareLink
import io.github.nathplay15.strataos.net.createShareLink
import io.github.nathplay15.strataos.net.fileInfo
import io.github.nathplay15.strataos.net.linkUrl
import io.github.nathplay15.strataos.ui.InfoRow
import io.github.nathplay15.strataos.ui.Loaded
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.errorText
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad

fun copyText(ctx: Context, label: String, text: String) {
    (ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager).setPrimaryClip(ClipData.newPlainText(label, text))
}

fun shareText(ctx: Context, text: String) {
    ctx.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text), null))
}

@Composable
private fun SwitchLine(label: String, checked: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Text(label, Modifier.weight(1f))
        Switch(checked, onChange, enabled = enabled)
    }
}

/** Creates a share link for a file, a folder or an album, then shows its address. */
@Composable
fun ShareLinkDialog(path: String = "", album: String = "", dir: Boolean = false, writable: Boolean = false, title: String, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    val act = rememberActions()
    var download by remember { mutableStateOf(true) }
    var upload by remember { mutableStateOf(false) }
    var password by remember { mutableStateOf("") }
    var days by remember { mutableStateOf("7") }
    var max by remember { mutableStateOf("") }
    var made by remember { mutableStateOf<ShareLink?>(null) }
    val link = made
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.a_share_link_for, title)) },
        text = {
            if (link != null) {
                val u = api().linkUrl(link.id)
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(stringResource(R.string.a_link_ready))
                    Text(u, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
                    Row {
                        TextButton(onClick = { copyText(ctx, title, u) }) { Text(stringResource(R.string.a_copy_link)) }
                        TextButton(onClick = { shareText(ctx, u) }) { Text(stringResource(R.string.share)) }
                    }
                }
            } else Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (album.isEmpty()) {
                    SwitchLine(stringResource(R.string.a_link_download), download) { download = it }
                    if (dir) SwitchLine(stringResource(R.string.a_link_upload), upload, enabled = writable) { upload = it }
                }
                OutlinedTextField(password, { password = it }, label = { Text(stringResource(R.string.a_link_password)) }, singleLine = true,
                    visualTransformation = PasswordVisualTransformation(), modifier = Modifier.fillMaxWidth(),
                    supportingText = { Text(stringResource(R.string.a_link_password_hint)) })
                OutlinedTextField(days, { days = it.filter(Char::isDigit).take(4) }, label = { Text(stringResource(R.string.a_link_days)) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), modifier = Modifier.fillMaxWidth(),
                    supportingText = { Text(stringResource(R.string.a_link_days_hint)) })
                if (album.isEmpty()) OutlinedTextField(max, { max = it.filter(Char::isDigit).take(6) }, label = { Text(stringResource(R.string.a_link_max)) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number), modifier = Modifier.fillMaxWidth())
            }
        },
        confirmButton = {
            if (link == null) TextButton(
                enabled = !act.busy.value && (download || upload || album.isNotEmpty()) && (password.isEmpty() || password.length >= 4),
                onClick = {
                    act.run {
                        made = api().createShareLink(NewLink(path = path, album = album, download = download || album.isNotEmpty(), upload = upload,
                            password = password, expiresDays = days.toIntOrNull() ?: 0, maxDownloads = max.toIntOrNull() ?: 0))
                    }
                },
            ) { Text(stringResource(R.string.create)) }
            else TextButton(onClick = onDismiss) { Text(stringResource(R.string.a_close)) }
        },
        dismissButton = { if (link == null) TextButton(onClick = onDismiss) { Text(stringResource(R.string.cancel)) } },
    )
}


/** Details of a NAS item (owner, rights, type) and its state on this phone. */
@Composable
fun PropertiesDialog(e: FileEntry, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    val info = rememberLoad(e.path) { api().fileInfo(e.path) }
    val fav = remember { favorites.get(e.path) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(e.name) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState())) {
                InfoRow(stringResource(R.string.a_prop_location), displayPath(e.path.substringBeforeLast('/')))
                when (val s = info.state) {
                    is Loaded.Ok -> {
                        val i = s.value
                        InfoRow(stringResource(R.string.a_prop_type), if (i.dir) stringResource(R.string.a_folder) else i.mime)
                        if (!i.dir) InfoRow(stringResource(R.string.a_prop_size), formatBytes(i.size) + " (" + i.size + ")")
                        InfoRow(stringResource(R.string.a_prop_modified), formatDate(i.modTime))
                        InfoRow(stringResource(R.string.a_prop_owner), listOf(i.owner, i.group).filter { it.isNotEmpty() }.joinToString(" / "))
                        InfoRow(stringResource(R.string.a_prop_mode), i.mode)
                        InfoRow(stringResource(R.string.a_prop_access), stringResource(if (i.writable) R.string.a_read_write else R.string.a_read_only))
                    }
                    is Loaded.Failed -> Text(errorText(ctx, s.error), color = MaterialTheme.colorScheme.error)
                    Loaded.Loading -> Text("…")
                }
                if (fav != null) InfoRow(stringResource(R.string.a_prop_phone), stringResource(if (fav.offline) R.string.a_offline_kept else R.string.a_favorite))
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.a_close)) } },
    )
}
