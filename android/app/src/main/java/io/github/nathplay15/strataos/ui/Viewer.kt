package io.github.nathplay15.strataos.ui

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.ImageDecoder
import android.graphics.pdf.PdfRenderer
import android.net.Uri
import android.os.Build
import android.os.ParcelFileDescriptor
import android.widget.MediaController
import android.widget.VideoView
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material.icons.filled.Share
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.FileEntry
import java.io.File

/** In-app preview of images, videos and PDF files (downloaded to the cache first). */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun Viewer(e: FileEntry, f: File, onClose: () -> Unit) {
    val ctx = LocalContext.current
    BackHandler(onBack = onClose)
    Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), topBar = {
        TopAppBar(
            title = { Text(e.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
            navigationIcon = { IconButton(onClick = onClose) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
            actions = {
                IconButton(onClick = { shareFile(ctx, f) }) { Icon(Icons.Default.Share, stringResource(R.string.share)) }
                IconButton(onClick = { openWith(ctx, f) }) { Icon(Icons.AutoMirrored.Filled.OpenInNew, stringResource(R.string.open_with)) }
            },
        )
    }) { pad ->
        Box(Modifier.padding(pad).fillMaxSize().background(Color.Black), contentAlignment = Alignment.Center) {
            when (kindOf(e.name)) {
                Kind.IMAGE -> ImageView(f)
                Kind.VIDEO -> VideoPlayer(f)
                Kind.PDF -> PdfView(f)
                Kind.OTHER -> Unit
            }
        }
    }
}

/** Decodes at most ~4096 px wide (large photos would not fit in memory). */
private fun decode(f: File): Bitmap? = runCatching {
    if (Build.VERSION.SDK_INT >= 28) {
        ImageDecoder.decodeBitmap(ImageDecoder.createSource(f)) { d, info, _ ->
            val s = maxOf(info.size.width, info.size.height)
            if (s > 4096) d.setTargetSampleSize((s + 4095) / 4096)
        }
    } else {
        val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(f.path, o)
        var sample = 1
        while (maxOf(o.outWidth, o.outHeight) / sample > 4096) sample *= 2
        BitmapFactory.decodeFile(f.path, BitmapFactory.Options().apply { inSampleSize = sample })
    }
}.getOrNull()

@Suppress("DEPRECATION") // the centroid variant is not needed for a simple zoom
@Composable
private fun ImageView(f: File) {
    val bmp = remember(f) { decode(f) }
    var scale by remember { mutableFloatStateOf(1f) }
    var offset by remember { mutableStateOf(androidx.compose.ui.geometry.Offset.Zero) }
    val state = rememberTransformableState { z, pan, _ -> scale = (scale * z).coerceIn(1f, 8f); offset += pan }
    if (bmp == null) {
        Text(stringResource(R.string.cannot_preview), color = Color.White)
        return
    }
    Image(bmp.asImageBitmap(), null, contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize().transformable(state)
        .graphicsLayer(scaleX = scale, scaleY = scale, translationX = offset.x, translationY = offset.y))
}

@Composable
private fun VideoPlayer(f: File) {
    AndroidView(factory = { c ->
        VideoView(c).apply {
            setMediaController(MediaController(c).also { it.setAnchorView(this) })
            setVideoURI(Uri.fromFile(f))
            setOnPreparedListener { start() }
        }
    }, modifier = Modifier.fillMaxWidth())
}

@Composable
private fun PdfView(f: File) {
    val renderer = remember(f) { runCatching { PdfRenderer(ParcelFileDescriptor.open(f, ParcelFileDescriptor.MODE_READ_ONLY)) }.getOrNull() }
    DisposableEffect(renderer) { onDispose { renderer?.close() } }
    if (renderer == null) {
        Text(stringResource(R.string.cannot_preview), color = Color.White)
        return
    }
    val width = LocalContext.current.resources.displayMetrics.widthPixels
    LazyColumn(Modifier.fillMaxSize().background(Color.DarkGray)) {
        items(renderer.pageCount) { i ->
            val page = remember(i) {
                synchronized(renderer) {
                    renderer.openPage(i).use { p ->
                        val h = (width.toFloat() * p.height / p.width).toInt()
                        Bitmap.createBitmap(width, h, Bitmap.Config.ARGB_8888).also {
                            it.eraseColor(android.graphics.Color.WHITE)
                            p.render(it, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                        }
                    }
                }
            }
            Image(page.asImageBitmap(), null, Modifier.fillMaxWidth().padding(bottom = 6.dp), contentScale = ContentScale.FillWidth)
        }
    }
}
