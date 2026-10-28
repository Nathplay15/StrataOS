package io.github.nathplay15.strataos.ui.files

import android.content.Context
import android.net.Uri
import android.provider.DocumentsContract
import io.github.nathplay15.strataos.data.favorites
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.mimeOf
import java.io.File

/** The name a download gets (folders arrive as ZIP files). */
fun downloadName(e: FileEntry) = if (e.dir) e.name + ".zip" else e.name

/**
 * A local copy of a NAS file: the offline copy when it is current, else the
 * cache (downloaded again when the size changed). Runs on an IO thread.
 */
fun localCopy(ctx: Context, e: FileEntry, progress: (Float) -> Unit = {}): File {
    favorites.usableCopy(e.path, e.size, e.modTime)?.let { return it }
    val dir = File(ctx.cacheDir, "open/" + Integer.toHexString(e.path.hashCode())).apply { mkdirs() }
    val f = File(dir, downloadName(e))
    if (e.dir || !(f.exists() && f.length() == e.size)) {
        api().download(e.path, f) { d, t -> if (t > 0) progress(d.toFloat() / t) }
    }
    return f
}

fun copyToUri(ctx: Context, f: File, uri: Uri) {
    ctx.contentResolver.openOutputStream(uri)!!.use { out -> f.inputStream().use { it.copyTo(out) } }
}

/** Saves NAS items into a folder the user picked (Storage Access Framework). */
fun saveToTree(ctx: Context, tree: Uri, entries: List<FileEntry>, progress: (Int) -> Unit) {
    val parent = DocumentsContract.buildDocumentUriUsingTree(tree, DocumentsContract.getTreeDocumentId(tree))
    entries.forEachIndexed { i, e ->
        progress(i)
        val f = localCopy(ctx, e)
        val name = downloadName(e)
        val doc = DocumentsContract.createDocument(ctx.contentResolver, parent, if (e.dir) "application/zip" else mimeOf(name), name)
            ?: throw java.io.IOException("cannot create $name")
        copyToUri(ctx, f, doc)
    }
}
