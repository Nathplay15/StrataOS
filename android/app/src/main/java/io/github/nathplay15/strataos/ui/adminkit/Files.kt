package io.github.nathplay15.strataos.ui.adminkit

import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.net.Uri
import android.util.LruCache
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.BrokenImage
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalResources
import androidx.core.content.FileProvider
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.manage.bytes
import java.io.File
import java.io.IOException
import java.io.OutputStream
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl

// Pictures and files of the NAS for the management areas: authenticated
// image loading, and saving or sharing what the NAS sends (clips, VPN files).

private val thumbs = object : LruCache<String, ImageBitmap>(24 * 1024 * 1024) {
    override fun sizeOf(key: String, value: ImageBitmap) = value.width * value.height * 4
}

/** Decodes a JPEG/PNG at most [maxSide] pixels wide or high. */
fun decodeImage(data: ByteArray, maxSide: Int = 1280): ImageBitmap? {
    val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(data, 0, data.size, o)
    var sample = 1
    while (o.outWidth / (sample * 2) >= maxSide || o.outHeight / (sample * 2) >= maxSide) sample *= 2
    return BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })?.asImageBitmap()
}

/** A picture of the NAS loaded with the device token ([cache]: kept in memory by URL). */
@Composable
fun AuthImage(url: HttpUrl, description: String?, modifier: Modifier = Modifier, cache: Boolean = true, contentScale: ContentScale = ContentScale.Crop) {
    val key = url.toString()
    var img by remember(key) { mutableStateOf(if (cache) thumbs.get(key) else null) }
    var failed by remember(key) { mutableStateOf(false) }
    LaunchedEffect(key) {
        if (img != null) return@LaunchedEffect
        img = runCatching { withContext(Dispatchers.IO) { decodeImage(api().bytes(url), 640) } }.getOrNull()
        if (img == null) failed = true else if (cache) thumbs.put(key, img)
    }
    Box(modifier.background(MaterialTheme.colorScheme.surfaceVariant), contentAlignment = Alignment.Center) {
        val i = img
        when {
            i != null -> Image(i, description, Modifier.matchParentSize(), contentScale = contentScale)
            failed -> Icon(Icons.Default.BrokenImage, description, tint = MaterialTheme.colorScheme.onSurfaceVariant)
            else -> CircularProgressIndicator()
        }
    }
}

/** Copies a GET of the NAS to [out]. */
fun Api.copyTo(url: HttpUrl, out: OutputStream) {
    http.newCall(request(url).get().build()).execute().use { r ->
        if (!r.isSuccessful) throw Api.apiError(r.code, r.body.string())
        r.body.byteStream().use { it.copyTo(out, 64 * 1024) }
    }
}

/** A file name the phone accepts. */
fun safeName(name: String): String = name.replace(Regex("[\\\\/:*?\"<>|\\x00-\\x1f]"), "_").trim().ifEmpty { "file" }.take(120)

/** Folder of the files handed to other apps (under the FileProvider's "open/" path). */
fun sharedDir(ctx: Context): File = File(ctx.cacheDir, "open/manage").apply { mkdirs() }

fun shareFile(ctx: Context, f: File, mime: String, title: String? = null) {
    val uri = FileProvider.getUriForFile(ctx, ctx.packageName + ".files", f)
    val send = Intent(Intent.ACTION_SEND).setType(mime).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    send.clipData = ClipData.newRawUri(f.name, uri)
    ctx.startActivity(Intent.createChooser(send, title ?: f.name).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
}

/** Downloads [url] into the shared folder and opens the share sheet. */
suspend fun downloadAndShare(ctx: Context, url: HttpUrl, name: String, mime: String) {
    val f = File(sharedDir(ctx), safeName(name))
    withContext(Dispatchers.IO) {
        val tmp = File(f.path + ".part")
        tmp.outputStream().use { api().copyTo(url, it) }
        if (!tmp.renameTo(f)) throw IOException("cannot write ${f.name}")
    }
    shareFile(ctx, f, mime)
}

/** Writes [text] to a shared file and opens the share sheet (VPN configurations). */
fun shareTextFile(ctx: Context, name: String, text: String, mime: String = "text/plain") {
    val f = File(sharedDir(ctx), safeName(name))
    f.writeText(text)
    shareFile(ctx, f, mime)
}

/**
 * "Save as": the system file picker, then [write] into the chosen document.
 * Returns the function that opens the picker with a suggested name.
 */
@Composable
fun rememberSaveAs(mime: String, write: suspend (Uri) -> Unit): (String) -> Unit {
    val res = LocalResources.current
    val actions = rememberActions()
    val launcher = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument(mime)) { uri ->
        if (uri != null) actions.run(done = res.getString(io.github.nathplay15.strataos.R.string.b_saved)) { write(uri) }
    }
    return { name -> launcher.launch(safeName(name)) }
}

/** Streams [url] of the NAS into a document chosen with [rememberSaveAs]. */
fun saveUrlTo(ctx: Context, url: HttpUrl, uri: Uri) {
    ctx.contentResolver.openOutputStream(uri)?.use { api().copyTo(url, it) } ?: throw IOException("cannot write the file")
}
