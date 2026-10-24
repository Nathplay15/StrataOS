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
import androidx.compose.ui.platform.LocalResources
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

enum class Kind { IMAGE, VIDEO, PDF, OTHER }

fun kindOf(name: String): Kind = when (name.substringAfterLast('.', "").lowercase()) {
    "jpg", "jpeg", "png", "gif", "webp", "bmp", "heic", "heif", "avif" -> Kind.IMAGE
    "mp4", "m4v", "mov", "webm", "mkv", "3gp" -> Kind.VIDEO
    "pdf" -> Kind.PDF
    else -> Kind.OTHER
}

fun mimeOf(name: String): String =
    MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase()) ?: "application/octet-stream"

private fun iconOf(e: FileEntry): ImageVector = if (e.dir) Icons.Default.Folder else when (kindOf(e.name)) {
    Kind.IMAGE -> Icons.Default.Image
    Kind.VIDEO -> Icons.Default.Movie
    Kind.PDF -> Icons.Default.PictureAsPdf
    Kind.OTHER -> Icons.Default.Description
}

/** Cache file for a NAS file (cleared by the system when space is needed). */
fun cacheFor(ctx: Context, e: FileEntry): File {
    val dir = File(ctx.cacheDir, "open/" + Integer.toHexString(e.path.hashCode()))
    dir.mkdirs()
    return File(dir, e.name)
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FilesScreen(path: String, setPath: (String) -> Unit) {
    val ctx = LocalContext.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val loader = rememberLoader(path) { Graph.api()!!.files(path) }
    var viewing by remember { mutableStateOf<Pair<FileEntry, File>?>(null) }
    var progress by remember { mutableStateOf<Pair<String, Float>?>(null) }
    var menuFor by remember { mutableStateOf<FileEntry?>(null) }
    var newFolder by remember { mutableStateOf(false) }
    var saving by remember { mutableStateOf<FileEntry?>(null) }
    val list = (loader.state as? Loaded.Ok)?.value

    viewing?.let { (e, f) ->
        Viewer(e, f) { viewing = null }
        return
    }

    BackHandler(enabled = path.isNotEmpty() && list != null && list.path != "/srv/strata") {
        setPath(list!!.path.substringBeforeLast('/').ifEmpty { "" })
    }

    fun fetch(e: FileEntry, then: (File) -> Unit) {
        scope.launch {
            val f = cacheFor(ctx, e)
            try {
                if (!(f.exists() && f.length() == e.size)) {
                    progress = e.name to 0f
                    withContext(Dispatchers.IO) {
                        Graph.api()!!.download(e.path, f) { d, t -> if (t > 0) progress = e.name to d.toFloat() / t }
                    }
                }
                then(f)
            } catch (ex: Exception) {
                Toast.makeText(ctx, errorText(ctx, ex), Toast.LENGTH_LONG).show()
            } finally {
                progress = null
            }
        }
    }

    fun open(e: FileEntry) = fetch(e) { f ->
        if (kindOf(e.name) != Kind.OTHER) viewing = e to f else openWith(ctx, f)
    }

    val saveTo = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("*/*")) { uri ->
        val e = saving ?: return@rememberLauncherForActivityResult
        saving = null
        if (uri != null) fetch(e) { f ->
            scope.launch(Dispatchers.IO) {
                runCatching { ctx.contentResolver.openOutputStream(uri)!!.use { out -> f.inputStream().use { it.copyTo(out) } } }
                    .onSuccess { withContext(Dispatchers.Main) { Toast.makeText(ctx, res.getString(R.string.saved), Toast.LENGTH_SHORT).show() } }
            }
        }
    }

    val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        val folder = list?.path ?: return@rememberLauncherForActivityResult
        if (uris.isNotEmpty()) scope.launch {
            progress = res.getString(R.string.preparing) to 0f
            val dir = withContext(Dispatchers.IO) { stageUris(ctx, uris) }
            progress = null
            UploadWorker.enqueue(ctx, folder, dir)
            Toast.makeText(ctx, res.getString(R.string.upload_started, uris.size), Toast.LENGTH_SHORT).show()
        }
    }

    Scaffold(
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
        topBar = {
            TopAppBar(
                title = { Text(list?.path?.removePrefix("/srv/strata")?.ifEmpty { null }?.trim('/') ?: stringResource(R.string.tab_files), maxLines = 1, overflow = TextOverflow.Ellipsis) },
                navigationIcon = {
                    if (list != null && list.path != "/srv/strata") IconButton(onClick = { setPath(list.path.substringBeforeLast('/')) }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back))
                    }
                },
                actions = {
                    if (list?.writable == true) IconButton(onClick = { newFolder = true }) { Icon(Icons.Default.CreateNewFolder, stringResource(R.string.new_folder)) }
                    IconButton(onClick = loader.reload) { Icon(Icons.Default.Refresh, stringResource(R.string.refresh)) }
                },
            )
        },
        floatingActionButton = {
            if (list?.writable == true) FloatingActionButton(onClick = { pick.launch("*/*") }) { Icon(Icons.Default.Upload, stringResource(R.string.upload)) }
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            progress?.let { (name, p) ->
                Text(name, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(horizontal = 16.dp))
                LinearProgressIndicator(progress = { p }, modifier = Modifier.fillMaxWidth().padding(16.dp, 4.dp))
            }
            LoadedContent(loader) { l ->
                if (l.entries.isEmpty()) {
                    Box(Modifier.fillMaxSize().padding(32.dp)) { Text(stringResource(R.string.empty_folder), color = MaterialTheme.colorScheme.onSurfaceVariant) }
                } else LazyColumn(Modifier.fillMaxSize()) {
                    items(l.entries, key = { it.path }) { e ->
                        Box {
                            ListItem(
                                headlineContent = { Text(e.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                                supportingContent = { Text(if (e.dir) formatDate(e.modTime) else formatBytes(e.size) + " · " + formatDate(e.modTime)) },
                                leadingContent = { Icon(iconOf(e), null, tint = if (e.dir) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant) },
                                modifier = Modifier.combinedClickable(onClick = { if (e.dir) setPath(e.path) else open(e) }, onLongClick = { if (!e.dir) menuFor = e }),
                            )
                            DropdownMenu(expanded = menuFor == e, onDismissRequest = { menuFor = null }) {
                                DropdownMenuItem(text = { Text(stringResource(R.string.open)) }, onClick = { menuFor = null; open(e) })
                                DropdownMenuItem(text = { Text(stringResource(R.string.open_with)) }, onClick = { menuFor = null; fetch(e) { openWith(ctx, it) } })
                                DropdownMenuItem(text = { Text(stringResource(R.string.save_to_phone)) }, onClick = { menuFor = null; saving = e; saveTo.launch(e.name) })
                                DropdownMenuItem(text = { Text(stringResource(R.string.share)) }, onClick = { menuFor = null; fetch(e) { shareFile(ctx, it) } })
                            }
                        }
                        HorizontalDivider()
                    }
                }
            }
        }
    }

    if (newFolder) {
        var name by remember { mutableStateOf("") }
        AlertDialog(
            onDismissRequest = { newFolder = false },
            title = { Text(stringResource(R.string.new_folder)) },
            text = { OutlinedTextField(name, { name = it }, singleLine = true, label = { Text(stringResource(R.string.name)) }) },
            confirmButton = {
                TextButton(enabled = name.isNotBlank() && !name.contains('/'), onClick = {
                    newFolder = false
                    val p = list!!.path + "/" + name.trim()
                    scope.launch {
                        runCatching { withContext(Dispatchers.IO) { Graph.api()!!.mkdir(p) } }
                            .onSuccess { loader.reload() }
                            .onFailure { Toast.makeText(ctx, errorText(ctx, it), Toast.LENGTH_LONG).show() }
                    }
                }) { Text(stringResource(R.string.create)) }
            },
            dismissButton = { TextButton(onClick = { newFolder = false }) { Text(stringResource(R.string.cancel)) } },
        )
    }
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
