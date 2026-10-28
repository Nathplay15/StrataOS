package io.github.nathplay15.strataos.ui

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.LruCache
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.size
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
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import java.io.File
import java.security.MessageDigest
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl

/**
 * Thumbnails of the NAS: memory cache, then a disk cache in the app's cache
 * folder (the system clears it when space runs low), then the NAS, a few at a
 * time. Kept small on purpose: no image library for a handful of grids.
 */
object Thumbs {
    private val mem = object : LruCache<String, Bitmap>((Runtime.getRuntime().maxMemory() / 8).toInt()) {
        override fun sizeOf(key: String, value: Bitmap) = value.allocationByteCount
    }
    private val gate = Semaphore(4)
    private val failed = java.util.Collections.synchronizedSet(HashSet<String>())

    fun cached(url: HttpUrl): Bitmap? = mem.get(url.toString())

    private fun diskFile(key: String): File {
        val h = MessageDigest.getInstance("SHA-1").digest(key.toByteArray()).joinToString("") { "%02x".format(it) }
        return File(Graph.app.cacheDir, "thumbs/${h.take(2)}/$h")
    }

    /** The first of [urls] the NAS can answer (previews may be missing: other sources follow). */
    suspend fun loadFirst(urls: List<HttpUrl>, maxPx: Int): Bitmap? {
        for (u in urls) load(u, maxPx)?.let { return it }
        return null
    }

    /** The picture at [url], decoded to at most about [maxPx] pixels; null when the NAS has none. */
    suspend fun load(url: HttpUrl, maxPx: Int): Bitmap? {
        val key = url.toString()
        mem.get(key)?.let { return it }
        if (key in failed) return null
        return withContext(Dispatchers.IO) {
            val f = diskFile(key)
            val bytes = if (f.exists()) runCatching { f.readBytes() }.getOrNull() else null
            val data = bytes ?: gate.withPermit { fetch(url) }?.also { b ->
                runCatching { f.parentFile?.mkdirs(); File(f.path + ".tmp").apply { writeBytes(b) }.renameTo(f) }
            }
            if (data == null) {
                failed.add(key)
                return@withContext null
            }
            decode(data, maxPx)?.also { mem.put(key, it) }
        }
    }

    private fun fetch(url: HttpUrl): ByteArray? {
        val api = Graph.api() ?: return null
        return runCatching {
            api.http.newCall(api.request(url).build()).execute().use { r -> if (r.isSuccessful) r.body.bytes() else null }
        }.getOrNull()
    }

    fun decode(data: ByteArray, maxPx: Int): Bitmap? {
        val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(data, 0, data.size, o)
        if (o.outWidth <= 0) return null
        return BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sampleSize(o.outWidth, o.outHeight, maxPx) })
    }

    /** Forgets failures (after a refresh: the NAS may have made the thumbnail since). */
    fun retryFailed() = failed.clear()
}

/** The power of two that brings the larger side close to [maxPx] (never below it). */
fun sampleSize(w: Int, h: Int, maxPx: Int): Int {
    var s = 1
    while (maxOf(w, h) / (s * 2) >= maxPx) s *= 2
    return s
}

/** A thumbnail of the NAS, with [fallback] while loading or when there is none. */
@Composable
fun NasImage(
    url: HttpUrl?,
    contentDescription: String?,
    modifier: Modifier = Modifier,
    maxPx: Int = 400,
    contentScale: ContentScale = ContentScale.Crop,
    fallback: ImageVector? = null,
    alternatives: List<HttpUrl> = emptyList(),
) {
    var bmp by remember(url) { mutableStateOf(url?.let { Thumbs.cached(it) }) }
    LaunchedEffect(url) { if (bmp == null && url != null) bmp = Thumbs.loadFirst(listOf(url) + alternatives, maxPx) }
    val b = bmp
    if (b != null) {
        Image(b.asImageBitmap(), contentDescription, modifier, contentScale = contentScale)
    } else {
        Box(modifier.background(MaterialTheme.colorScheme.surfaceVariant), contentAlignment = Alignment.Center) {
            if (fallback != null) Icon(fallback, contentDescription, Modifier.size(28.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            else Box(Modifier.fillMaxSize())
        }
    }
}
