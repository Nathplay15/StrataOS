package io.github.nathplay15.strataos.ui

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.Dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.net.manage.rasterIconUrl
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Request

/** The icon of an app, fetched without credentials (it is a public address); [fallback] while loading or on failure. */
@Composable
fun AppIcon(icon: String, fallback: ImageVector, size: Dp) {
    var bmp by remember(icon) { mutableStateOf<Bitmap?>(null) }
    LaunchedEffect(icon) { if (bmp == null && icon.isNotEmpty()) bmp = loadIcon(icon) }
    val b = bmp
    if (b != null) Image(b.asImageBitmap(), null, Modifier.size(size))
    else Icon(fallback, null, Modifier.size(size), tint = MaterialTheme.colorScheme.primary)
}

private suspend fun loadIcon(icon: String): Bitmap? = withContext(Dispatchers.IO) {
    val u = rasterIconUrl(icon)?.toHttpUrlOrNull() ?: return@withContext null
    val http = Graph.api()?.http ?: return@withContext null
    runCatching {
        http.newCall(Request.Builder().url(u).build()).execute().use { r ->
            if (!r.isSuccessful) null else BitmapFactory.decodeStream(r.body.byteStream())
        }
    }.getOrNull()
}
