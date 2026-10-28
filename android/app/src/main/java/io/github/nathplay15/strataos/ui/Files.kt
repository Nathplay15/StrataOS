package io.github.nathplay15.strataos.ui

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import android.widget.Toast
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.CreateNewFolder
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Image
import androidx.compose.material.icons.filled.Movie
import androidx.compose.material.icons.filled.MusicNote
import androidx.compose.material.icons.filled.PictureAsPdf
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.upload.UploadWorker
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

enum class Kind { IMAGE, VIDEO, AUDIO, PDF, OTHER }

fun kindOf(name: String): Kind = when (name.substringAfterLast('.', "").lowercase()) {
    "jpg", "jpeg", "png", "gif", "webp", "bmp", "heic", "heif", "avif" -> Kind.IMAGE
    "mp4", "m4v", "mov", "webm", "mkv", "3gp", "avi", "wmv", "ts", "m2ts", "mts", "mpg", "mpeg", "flv", "ogv", "vob" -> Kind.VIDEO
    "mp3", "m4a", "aac", "flac", "ogg", "oga", "opus", "wav", "wma", "alac", "aiff", "aif", "mka", "ape" -> Kind.AUDIO
    "pdf" -> Kind.PDF
    else -> Kind.OTHER
}

fun mimeOf(name: String): String =
    MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase()) ?: "application/octet-stream"

fun iconOf(e: FileEntry): ImageVector = if (e.dir) Icons.Default.Folder else when (kindOf(e.name)) {
    Kind.IMAGE -> Icons.Default.Image
    Kind.VIDEO -> Icons.Default.Movie
    Kind.AUDIO -> Icons.Default.MusicNote
    Kind.PDF -> Icons.Default.PictureAsPdf
    Kind.OTHER -> Icons.Default.Description
}

/** Cache file for a NAS file (cleared by the system when space is needed). */
fun cacheFor(ctx: Context, e: FileEntry): File {
    val dir = File(ctx.cacheDir, "open/" + Integer.toHexString(e.path.hashCode()))
    dir.mkdirs()
    return File(dir, e.name)
}

fun uriFor(ctx: Context, f: File): Uri = FileProvider.getUriForFile(ctx, ctx.packageName + ".files", f)

fun openWith(ctx: Context, f: File) {
    val i = Intent(Intent.ACTION_VIEW).setDataAndType(uriFor(ctx, f), mimeOf(f.name)).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    try {
        ctx.startActivity(Intent.createChooser(i, f.name))
    } catch (e: ActivityNotFoundException) {
        Toast.makeText(ctx, ctx.getString(R.string.no_app), Toast.LENGTH_LONG).show()
    }
}

fun shareFile(ctx: Context, f: File) {
    val i = Intent(Intent.ACTION_SEND).setType(mimeOf(f.name)).putExtra(Intent.EXTRA_STREAM, uriFor(ctx, f)).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    ctx.startActivity(Intent.createChooser(i, f.name))
}

/** Copies chosen or shared files into the app's storage for the upload worker (read grants do not outlive the activity). */
fun stageUris(ctx: Context, uris: List<Uri>): File {
    val dir = File(ctx.filesDir, "outbox/" + System.currentTimeMillis())
    dir.mkdirs()
    val used = HashSet<String>()
    for (u in uris) {
        val original = displayName(ctx, u)?.replace('/', '_')?.trimStart('.')?.ifEmpty { null } ?: "file"
        val dot = original.lastIndexOf('.')
        val base = if (dot > 0) original.substring(0, dot) else original
        val ext = if (dot > 0) original.substring(dot) else ""
        var name = original
        var n = 2
        while (!used.add(name)) name = "$base (${n++})$ext"
        runCatching { ctx.contentResolver.openInputStream(u)?.use { input -> File(dir, name).outputStream().use { input.copyTo(it) } } }
    }
    return dir
}

fun displayName(ctx: Context, u: Uri): String? =
    runCatching { ctx.contentResolver.query(u, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c -> if (c.moveToFirst()) c.getString(0) else null } }.getOrNull()
        ?: u.lastPathSegment?.substringAfterLast('/')
