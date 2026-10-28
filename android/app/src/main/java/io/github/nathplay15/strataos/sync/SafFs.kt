package io.github.nathplay15.strataos.sync

import android.content.ContentResolver
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.DocumentsContract.Document
import android.util.Base64
import java.io.FileInputStream
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.util.Locale

/**
 * A phone folder chosen with the system picker (Storage Access Framework),
 * reached through its documents provider with the permission the app keeps.
 * Providers give neither file IDs nor a way to set dates: renames made on the
 * phone are sent as a new file plus a deletion (to the recycle bin), and
 * downloaded files carry the date of their download.
 */
class SafFs(private val cr: ContentResolver, private val tree: Uri) : LocalFs {
    // Shared storage and memory cards ignore case on Android.
    override val caseInsensitive = true

    private val rootId: String = DocumentsContract.getTreeDocumentId(tree)
    /** Document IDs by relative path (lower-cased), filled by listings. */
    private val ids = HashMap<String, String>()

    private fun k(rel: String) = rel.lowercase(Locale.ROOT)
    private fun uri(id: String): Uri = DocumentsContract.buildDocumentUriUsingTree(tree, id)

    private val cols = arrayOf(Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE, Document.COLUMN_SIZE, Document.COLUMN_LAST_MODIFIED)

    private class Doc(val id: String, val name: String, val dir: Boolean, val size: Long, val mtime: Long)

    private fun children(id: String): List<Doc>? = try {
        cr.query(DocumentsContract.buildChildDocumentsUriUsingTree(tree, id), cols, null, null, null)?.use { c ->
            val out = ArrayList<Doc>()
            while (c.moveToNext()) {
                val name = c.getString(1) ?: continue
                val dir = c.getString(2) == Document.MIME_TYPE_DIR
                out += Doc(c.getString(0), name, dir, if (dir || c.isNull(3)) 0 else c.getLong(3), if (c.isNull(4)) 0 else c.getLong(4))
            }
            out
        }
    } catch (_: Exception) {
        null // permission revoked, card removed
    }

    private fun doc(id: String): Doc? = try {
        cr.query(uri(id), cols, null, null, null)?.use { c ->
            if (!c.moveToFirst()) null else {
                val dir = c.getString(2) == Document.MIME_TYPE_DIR
                Doc(c.getString(0), c.getString(1) ?: "", dir, if (dir || c.isNull(3)) 0 else c.getLong(3), if (c.isNull(4)) 0 else c.getLong(4))
            }
        }
    } catch (_: Exception) {
        null
    }

    /** The child of folder [parent] named [name] (exact spelling first). */
    private fun find(parent: String, name: String): Doc? {
        val l = children(parent) ?: return null
        return l.firstOrNull { it.name == name } ?: l.firstOrNull { it.name.equals(name, ignoreCase = true) }
    }

    /** The document ID of [rel], or null when absent. */
    private fun idOf(rel: String): String? {
        if (rel.isEmpty()) return rootId
        ids[k(rel)]?.let { id -> if (doc(id) != null) return id else forget(rel) }
        val parent = idOf(parentOf(rel)) ?: return null
        val d = find(parent, rel.substringAfterLast('/')) ?: return null
        ids[k(rel)] = d.id
        return d.id
    }

    private fun forget(rel: String) {
        val p = k(rel)
        ids.keys.removeAll { it == p || it.startsWith("$p/") }
    }

    override fun rootExists(): Boolean = doc(rootId)?.dir == true

    override fun list(rel: String): List<LocalChild>? {
        val id = idOf(rel) ?: return null
        val l = children(id) ?: return null
        val out = ArrayList<LocalChild>(l.size)
        for (d in l) {
            if (d.name.startsWith(OLD_PREFIX)) {
                recoverOld(rel, d, l)
                continue
            }
            ids[k(joinRel(rel, d.name))] = d.id
            out += LocalChild(d.name, d.dir, d.size, d.mtime)
        }
        return out
    }

    override fun stat(rel: String): LocalEntry? {
        val id = idOf(rel) ?: return null
        val d = doc(id) ?: return null
        return LocalEntry(rel, d.dir, if (d.dir) 0 else d.size, if (d.dir) 0 else d.mtime)
    }

    override fun mkdirs(rel: String) {
        if (rel.isEmpty() || idOf(rel) != null) return
        mkdirs(parentOf(rel))
        val parent = idOf(parentOf(rel)) ?: throw IOException("cannot create ${parentOf(rel)}")
        val u = DocumentsContract.createDocument(cr, uri(parent), Document.MIME_TYPE_DIR, rel.substringAfterLast('/'))
            ?: throw IOException("cannot create $rel")
        ids[k(rel)] = DocumentsContract.getDocumentId(u)
    }

    override fun move(from: String, to: String) {
        val id = idOf(from) ?: throw IOException("$from not found")
        val caseOnly = k(from) == k(to)
        if (!caseOnly && idOf(to) != null) throw IOException("$to exists")
        var u = uri(id)
        val fromParent = parentOf(from)
        val toParent = parentOf(to)
        if (k(fromParent) != k(toParent)) {
            mkdirs(toParent)
            val src = idOf(fromParent) ?: throw IOException("$fromParent not found")
            val dst = idOf(toParent) ?: throw IOException("$toParent not found")
            u = DocumentsContract.moveDocument(cr, u, uri(src), uri(dst)) ?: throw IOException("cannot move $from")
        }
        val name = to.substringAfterLast('/')
        if (from.substringAfterLast('/') != name) {
            if (caseOnly) {
                // Providers refuse a name that differs only by case: go through another one.
                u = DocumentsContract.renameDocument(cr, u, ".strataos-sync-case-" + System.nanoTime()) ?: throw IOException("cannot rename $from")
            }
            u = DocumentsContract.renameDocument(cr, u, name) ?: throw IOException("cannot rename $from")
        }
        forget(from)
        forget(to)
        ids[k(to)] = DocumentsContract.getDocumentId(u)
    }

    override fun openRead(rel: String, offset: Long): InputStream {
        val id = idOf(rel) ?: throw IOException("$rel not found")
        val pfd = cr.openFileDescriptor(uri(id), "r") ?: throw IOException("cannot open $rel")
        val s = FileInputStream(pfd.fileDescriptor)
        s.channel.position(offset)
        return object : java.io.FilterInputStream(s) {
            override fun close() { super.close(); pfd.close() }
        }
    }

    override fun openAppend(rel: String): OutputStream {
        val id = idOf(rel) ?: run {
            val parent = idOf(parentOf(rel)) ?: throw IOException("${parentOf(rel)} not found")
            val u = DocumentsContract.createDocument(cr, uri(parent), "application/octet-stream", rel.substringAfterLast('/'))
                ?: throw IOException("cannot create $rel")
            DocumentsContract.getDocumentId(u).also { ids[k(rel)] = it }
        }
        return cr.openOutputStream(uri(id), "wa") ?: throw IOException("cannot write $rel")
    }

    override fun delete(rel: String) {
        val id = idOf(rel) ?: return
        runCatching { DocumentsContract.deleteDocument(cr, uri(id)) }
        forget(rel)
    }

    override fun replace(part: String, target: String) {
        val partId = idOf(part) ?: throw IOException("$part not found")
        val name = target.substringAfterLast('/')
        val old = idOf(target)
        var oldUri: Uri? = null
        if (old != null) {
            // The old file steps aside under a name that says where it came
            // from: if the app dies before the end, the next listing puts it back.
            val enc = Base64.encodeToString(name.toByteArray(), Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
            oldUri = if (OLD_PREFIX.length + enc.length <= 250) {
                DocumentsContract.renameDocument(cr, uri(old), OLD_PREFIX + enc) ?: throw IOException("cannot replace $target")
            } else {
                DocumentsContract.deleteDocument(cr, uri(old))
                null
            }
            forget(target)
        }
        val u = DocumentsContract.renameDocument(cr, uri(partId), name) ?: throw IOException("cannot write $target")
        forget(part)
        ids[k(target)] = DocumentsContract.getDocumentId(u)
        oldUri?.let { runCatching { DocumentsContract.deleteDocument(cr, it) } }
    }

    private fun recoverOld(dir: String, d: Doc, siblings: List<Doc>) {
        val name = runCatching { String(Base64.decode(d.name.removePrefix(OLD_PREFIX), Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)) }.getOrNull()
        runCatching {
            if (name != null && name.isNotEmpty() && siblings.none { it.name.equals(name, ignoreCase = true) }) {
                DocumentsContract.renameDocument(cr, uri(d.id), name)?.let { ids[k(joinRel(dir, name))] = DocumentsContract.getDocumentId(it) }
            } else {
                DocumentsContract.deleteDocument(cr, uri(d.id))
            }
        }
    }

    override fun setMtime(rel: String, ms: Long) = Unit // documents providers keep their own dates

    override fun trash(rel: String, stamp: Long) {
        val dest = trashFolder(stamp) + (parentOf(rel).let { if (it.isEmpty()) "" else "/$it" })
        mkdirs(dest)
        var name = rel.substringAfterLast('/')
        var n = 2
        while (idOf("$dest/$name") != null) name = "${rel.substringAfterLast('/')} (${n++})"
        move(rel, "$dest/$name")
    }

    override fun purgeTrash(before: Long) {
        val t = idOf(TRASH_DIR) ?: return
        for (d in children(t).orEmpty()) {
            val at = trashTime(d.name) ?: continue
            if (at < before) runCatching { DocumentsContract.deleteDocument(cr, uri(d.id)) }
        }
        forget(TRASH_DIR)
    }

    companion object {
        const val OLD_PREFIX = ".strataos-sync-old-"
    }
}
