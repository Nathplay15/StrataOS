package io.github.nathplay15.strataos.backup

import android.Manifest
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import androidx.core.content.ContextCompat
import io.github.nathplay15.strataos.upload.ChunkSource
import java.io.IOException
import java.io.InputStream

data class Album(val id: String, val name: String, val count: Int)

/** Reads photos and videos from MediaStore (read only: the backup never deletes anything on the phone). */
object MediaScanner {
    private val collections = listOf(
        MediaStore.Images.Media.EXTERNAL_CONTENT_URI to false,
        MediaStore.Video.Media.EXTERNAL_CONTENT_URI to true,
    )

    fun hasPermission(ctx: Context): Boolean =
        if (Build.VERSION.SDK_INT >= 33) {
            granted(ctx, Manifest.permission.READ_MEDIA_IMAGES) || granted(ctx, Manifest.permission.READ_MEDIA_VIDEO) ||
                (Build.VERSION.SDK_INT >= 34 && granted(ctx, Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
        } else granted(ctx, Manifest.permission.READ_EXTERNAL_STORAGE)

    private fun granted(ctx: Context, p: String) = ContextCompat.checkSelfPermission(ctx, p) == PackageManager.PERMISSION_GRANTED

    fun permissions(): Array<String> = when {
        Build.VERSION.SDK_INT >= 33 -> arrayOf(Manifest.permission.READ_MEDIA_IMAGES, Manifest.permission.READ_MEDIA_VIDEO, Manifest.permission.ACCESS_MEDIA_LOCATION)
        Build.VERSION.SDK_INT >= 29 -> arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE, Manifest.permission.ACCESS_MEDIA_LOCATION)
        else -> arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE)
    }

    fun albums(ctx: Context): List<Album> {
        val m = linkedMapOf<String, Pair<String, Int>>()
        for (item in scan(ctx, null, true)) {
            val (name, n) = m[item.bucketId] ?: (item.bucket to 0)
            m[item.bucketId] = name to n + 1
        }
        return m.map { (id, v) -> Album(id, v.first, v.second) }.sortedByDescending { it.count }
    }

    /** Every photo (and video) of the given albums (null: all). */
    fun scan(ctx: Context, buckets: Set<String>?, videos: Boolean): List<MediaItem> {
        val out = ArrayList<MediaItem>()
        for ((uri, video) in collections) {
            if (video && !videos) continue
            val proj = arrayOf(
                MediaStore.MediaColumns._ID, MediaStore.MediaColumns.BUCKET_ID, MediaStore.MediaColumns.BUCKET_DISPLAY_NAME,
                MediaStore.MediaColumns.DISPLAY_NAME, MediaStore.MediaColumns.SIZE, MediaStore.MediaColumns.DATE_TAKEN,
                MediaStore.MediaColumns.DATE_MODIFIED,
            )
            // Files still being written (IS_PENDING) are skipped until finished.
            val sel = if (Build.VERSION.SDK_INT >= 29) "${MediaStore.MediaColumns.IS_PENDING} = 0" else null
            ctx.contentResolver.query(uri, proj, sel, null, null)?.use { c ->
                while (c.moveToNext()) {
                    val bucketId = c.getString(1) ?: continue
                    if (buckets != null && bucketId !in buckets) continue
                    val id = c.getLong(0)
                    out += MediaItem(
                        id = id, uri = ContentUris.withAppendedId(uri, id).toString(), bucketId = bucketId,
                        bucket = c.getString(2) ?: "Phone", name = c.getString(3) ?: continue, size = c.getLong(4),
                        dateTaken = c.getLong(5), dateModified = c.getLong(6) * 1000, video = video,
                    )
                }
            }
        }
        return out
    }

    /** The original file (with its location data when the permission was given). */
    fun source(ctx: Context, item: MediaItem): ChunkSource = UriSource(ctx, original(ctx, Uri.parse(item.uri)), item.size)

    private fun original(ctx: Context, uri: Uri): Uri =
        if (Build.VERSION.SDK_INT >= 29 && granted(ctx, Manifest.permission.ACCESS_MEDIA_LOCATION)) MediaStore.setRequireOriginal(uri) else uri
}

/** A content URI read from an offset (reopened and skipped: content streams cannot seek). */
class UriSource(private val ctx: Context, private val uri: Uri, override val size: Long) : ChunkSource {
    override fun openAt(offset: Long): InputStream {
        val input = ctx.contentResolver.openInputStream(uri) ?: throw IOException("cannot open $uri")
        var left = offset
        while (left > 0) {
            val n = input.skip(left)
            if (n <= 0) {
                if (input.read() < 0) { input.close(); throw IOException("file shorter than expected") }
                left--
            } else left -= n
        }
        return input
    }
}
