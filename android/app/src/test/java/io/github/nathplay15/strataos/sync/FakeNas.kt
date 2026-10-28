package io.github.nathplay15.strataos.sync

import java.io.IOException
import java.io.OutputStream
import java.security.MessageDigest

/**
 * An in-memory NAS folder with the semantics of the Sync API (port of
 * desktop/internal/engine/fake_test.go): versions (inode, size, date), a
 * numbered change feed, conditional uploads kept on conflict, moves,
 * deletions to a recycle bin.
 */
class FakeNas {
    class F(val dir: Boolean, var data: ByteArray = ByteArray(0), var mtime: Long = 0, val ino: Long)
    class Up(val rel: String, val size: Long, val mtime: Long, var ifMatch: String, var data: ByteArray = ByteArray(0))

    val files = HashMap<String, F>()
    private var seq = 0
    private val changed = HashMap<String, Int>()
    private val deleted = HashMap<String, Boolean>()
    private var ino = 0L
    val uploads = HashMap<String, Up>()
    private var nextUp = 0
    val recycle = ArrayList<String>()

    // Failures injected once.
    var failDownloadAt = 0L
    var failUploadAt = 0L
    var onDownload: ((String) -> Unit)? = null
    var onUpload: ((String) -> Unit)? = null
    val calls = HashMap<String, Int>()

    private fun count(k: String) { calls[k] = (calls[k] ?: 0) + 1 }
    fun calls(k: String) = calls[k] ?: 0

    fun version(x: F) = if (x.dir) "" else "${x.ino}-${x.data.size}-${x.mtime}"

    fun entry(p: String, x: F): RemoteEntry =
        if (x.dir) RemoteEntry(p, dir = true, mtime = x.mtime, id = x.ino.toString())
        else RemoteEntry(p, false, x.data.size.toLong(), x.mtime, version(x), sha(x.data), x.ino.toString())

    private fun touch(p: String, del: Boolean) {
        seq++
        changed[p] = seq
        deleted[p] = del
    }

    /** Writes a file on the "NAS" (as SMB would), creating its folders. */
    @Synchronized fun put(p: String, content: String, mtime: Long) {
        mkdirs(parentOf(p))
        val x = files.getOrPut(p) { F(false, ino = ++ino) }
        x.data = content.toByteArray()
        x.mtime = mtime
        touch(p, false)
    }

    private fun mkdirs(d: String) {
        if (d.isEmpty()) return
        mkdirs(parentOf(d))
        if (files[d] == null) {
            files[d] = F(true, ino = ++ino)
            touch(d, false)
        }
    }

    @Synchronized fun remove(p: String) {
        for (q in files.keys.toList()) if (q == p || q.startsWith("$p/")) {
            files.remove(q)
            touch(q, true)
        }
    }

    @Synchronized fun rename(a: String, b: String) = renameLocked(a, b)

    private fun renameLocked(a: String, b: String) {
        for ((q, x) in files.entries.toList()) if (q == a || q.startsWith("$a/")) {
            val n = b + q.removePrefix(a)
            files.remove(q)
            touch(q, true)
            files[n] = x
            touch(n, false)
        }
    }

    @Synchronized fun get(p: String): String? = files[p]?.takeIf { !it.dir }?.let { String(it.data) }

    @Synchronized fun list(): String = files.map { (p, x) -> if (x.dir) "$p/" else p }.sorted().joinToString(" ")

    val remote = object : Remote {
        override fun changes(folder: String, cursor: String, waitSec: Int): Changes = synchronized(this@FakeNas) {
            count("changes")
            val reset = cursor.isEmpty()
            val since = if (reset) 0 else cursor.removePrefix("e:").toInt()
            val entries = ArrayList<RemoteEntry>()
            val del = ArrayList<String>()
            for (p in changed.filter { it.value > since }.keys.sorted()) {
                if (deleted[p] == true) {
                    if (!reset) del += p
                    continue
                }
                entries += entry(p, files[p]!!)
            }
            Changes("e:$seq", reset, false, entries, del)
        }

        override fun hashes(folder: String, paths: List<String>): Map<String, String> = synchronized(this@FakeNas) {
            paths.mapNotNull { p -> files[p]?.takeIf { !it.dir }?.let { p to sha(it.data) } }.toMap()
        }

        override fun download(folder: String, rel: String, version: String, offset: Long, out: OutputStream): Long {
            val hook: ((String) -> Unit)?
            synchronized(this@FakeNas) {
                count("download")
                hook = onDownload
                onDownload = null
            }
            hook?.invoke(rel) // a change on the NAS as the download starts
            val data: ByteArray
            val v: String
            val fail: Long
            synchronized(this@FakeNas) {
                val x = files[rel]?.takeIf { !it.dir } ?: throw NotFoundException(rel)
                v = version(x)
                data = x.data.copyOfRange(offset.toInt(), x.data.size)
                fail = failDownloadAt
                failDownloadAt = 0
            }
            if (version.isNotEmpty() && v != version) throw ChangedException()
            if (fail > 0 && fail < data.size) {
                out.write(data, 0, fail.toInt())
                throw IOException("connection reset")
            }
            out.write(data)
            return data.size.toLong()
        }

        override fun upload(folder: String, u: UploadReq): RemoteEntry {
            var up: Up?
            val id: String
            synchronized(this@FakeNas) {
                count("upload")
                up = uploads[u.resumeId]
            }
            if (up == null) {
                synchronized(this@FakeNas) {
                    id = "up" + ++nextUp
                    up = Up(u.rel, u.size, u.mtime, u.ifMatch)
                    uploads[id] = up!!
                }
                u.onId(id)
            } else {
                id = u.resumeId
                synchronized(this@FakeNas) { count("resumed") }
            }
            val off = up!!.data.size.toLong()
            val rest = u.open(off).use { it.readBytes() }
            onUpload?.let { h -> onUpload = null; h(u.rel) } // a change on the NAS while the file is sent
            synchronized(this@FakeNas) {
                val fail = failUploadAt
                if (fail > 0 && fail < rest.size) {
                    failUploadAt = 0
                    up!!.data += rest.copyOfRange(0, fail.toInt())
                    throw IOException("connection reset")
                }
                up!!.data += rest
                up!!.ifMatch = u.ifMatch
                return commitLocked(id, up!!.rel, up!!.ifMatch)
            }
        }

        override fun commit(folder: String, uploadId: String, rel: String, ifMatch: String): RemoteEntry = synchronized(this@FakeNas) {
            if (uploads[uploadId] == null) throw IOException("gone")
            commitLocked(uploadId, rel, ifMatch)
        }

        override fun mkdir(folder: String, rel: String): RemoteEntry = synchronized(this@FakeNas) {
            files[rel]?.let { if (!it.dir) throw ConflictException("a file has this name", entry(rel, it)) }
            mkdirs(rel)
            entry(rel, files[rel]!!)
        }

        override fun move(folder: String, from: String, to: String, ifMatch: String): RemoteEntry = synchronized(this@FakeNas) {
            count("move")
            val x = files[from] ?: throw ConflictException("gone")
            if (!x.dir && ifMatch.isNotEmpty() && version(x) != ifMatch) throw ConflictException("changed", entry(from, x))
            files[to]?.let { if (!from.equals(to, ignoreCase = true)) throw ConflictException("exists", entry(to, it)) }
            mkdirs(parentOf(to))
            renameLocked(from, to)
            entry(to, x)
        }

        override fun delete(folder: String, rel: String, ifMatch: String) = synchronized(this@FakeNas) {
            val x = files[rel] ?: return@synchronized
            if (x.dir) {
                if (files.keys.any { it.startsWith("$rel/") }) throw ConflictException("not empty")
            } else if (ifMatch != "*" && version(x) != ifMatch) throw ConflictException("changed", entry(rel, x))
            files.remove(rel)
            touch(rel, true)
            if (!x.dir) recycle += rel
        }
    }

    private fun commitLocked(id: String, rel: String, ifMatch: String): RemoteEntry {
        val up = uploads[id]!!
        val cur = files[rel]
        when {
            cur != null && cur.dir -> throw ConflictException("a folder has this name", entry(rel, cur), id)
            ifMatch == "*" -> {}
            cur != null && version(cur) != ifMatch -> throw ConflictException("changed", entry(rel, cur), id)
            cur == null && ifMatch.isNotEmpty() -> throw ConflictException("deleted", null, id)
        }
        mkdirs(parentOf(rel))
        val x = F(false, up.data, up.mtime, ++ino)
        files[rel] = x
        touch(rel, false)
        uploads.remove(id)
        return entry(rel, x)
    }

    companion object {
        fun sha(b: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(b).toHex()
    }
}

/** The state database in memory (copies in and out, like a real one). */
class MemoryStore : SyncStore {
    private val pairs = HashMap<String, PairData>()

    @Synchronized override fun load(pair: String): PairData {
        val d = pairs.getOrPut(pair) { PairData() }
        return PairData(HashMap(d.base), HashMap(d.remote.mapValues { it.value.copy() }), d.cursor, HashMap(d.uploads))
    }

    @Synchronized override fun write(pair: String, changes: List<Change>, cursor: String?) {
        val d = pairs.getOrPut(pair) { PairData() }
        for (c in changes) when (c.table) {
            Table.BASE -> if (c.value == null) d.base.remove(c.key) else d.base[c.key] = c.value as BaseEntry
            Table.REMOTE -> if (c.value == null) d.remote.remove(c.key) else d.remote[c.key] = (c.value as RemoteEntry).copy()
            Table.UPLOADS -> if (c.value == null) d.uploads.remove(c.key) else d.uploads[c.key] = c.value as UploadResume
        }
        if (cursor != null) d.cursor = cursor
    }

    @Synchronized override fun replaceRemote(pair: String, remote: Map<String, RemoteEntry>, cursor: String) {
        val d = pairs.getOrPut(pair) { PairData() }
        d.remote.clear()
        remote.forEach { (k, v) -> d.remote[k] = v.copy() }
        d.cursor = cursor
    }

    @Synchronized override fun drop(pair: String) { pairs.remove(pair) }
}
