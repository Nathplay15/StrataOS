package io.github.nathplay15.strataos.sync

import java.io.IOException
import java.io.OutputStream
import java.security.MessageDigest
import java.time.ZoneId

/** Settings of the engine. */
class SyncOptions(
    /** This phone's name, in conflict copies. */
    val device: String,
    val ignore: List<String> = DEFAULT_IGNORE,
    /** The deletion brake: a round deleting at least [brakeMin] files and more than [brakeRatio] of the files synchronized waits for the user. */
    val brakeMin: Int = 20,
    val brakeRatio: Double = 0.5,
    val now: () -> Long = System::currentTimeMillis,
    val zone: ZoneId = ZoneId.systemDefault(),
    /** Smaller uploads are not recorded for resuming (one chunk, and no database write per file). */
    val resumeMin: Long = 8L shl 20,
    /** Days deleted phone files stay in the pair's trash. */
    val trashDays: Int = 30,
    val log: (String) -> Unit = {},
)

/** The state of a pair while it synchronizes. */
data class Progress(val pending: Int = 0, val done: Int = 0, val current: String = "", val fraction: Float = 0f)

/** What a round did. [brake]: deletions waiting for the user. */
class RoundResult(
    val again: Boolean,
    val error: Throwable?,
    val brake: Brake?,
    val files: Int,
    val skipped: List<Skipped>,
    val links: Int,
    val transferred: Int,
)

/** The preconditions of an op no longer hold: it is planned again. */
private class Skip : Exception("changed since planned")

private class ScanResult(
    val entries: HashMap<String, LocalEntry> = HashMap(), // by key
    val unreadable: MutableList<String> = mutableListOf(), // keys of folders that could not be read
    val skipped: MutableMap<String, String> = HashMap(), // phone paths not synchronized, and why
    var links: Int = 0,
)

/**
 * Synchronizes one pair (desktop/internal/engine, pairRun). A round pulls the
 * NAS changes, scans the phone folder, plans and runs the ops; [round] is
 * called again while it says more work waits. [stopped] ends a round early
 * (transfers resume later).
 */
class PairSync(
    private val remote: Remote,
    private val store: SyncStore,
    private val opt: SyncOptions,
    val pair: SyncPair,
    private val fs: LocalFs,
    private val stopped: () -> Boolean = { false },
    private val onEvent: (SyncEvent) -> Unit = {},
    private val onProgress: (Progress) -> Unit = {},
) {
    private val n = Namer(fs.caseInsensitive)
    private val m = Matcher(n, opt.ignore, pair.exclude)
    private var data = store.load(pair.id)
    private val pending = ArrayList<Change>()
    private var progress = Progress()
    private var trees = HashMap<String, List<Op>>()
    private val extraSkipped = ArrayList<Skipped>()
    private var transferred = 0
    /** Conflicts of the rounds of this object. */
    var conflicts = 0
        private set

    /**
     * One synchronization. [allowDeletes]: the user let the braked deletions
     * through; [restore]: the user wants the files back instead.
     */
    fun round(allowDeletes: Boolean = false, restore: Boolean = false): RoundResult {
        extraSkipped.clear()
        transferred = 0
        var brake: Brake? = null
        var skipped: List<Skipped> = emptyList()
        var links = 0
        val (again, err) = try {
            val r = sync(allowDeletes, restore)
            brake = r.brake
            skipped = r.skipped
            links = r.links
            r.again to null
        } catch (e: Exception) {
            if (e !is StoppedException && e !is UnauthorizedException) opt.log("${pair.remote}: ${e.message}")
            (e is StoppedException) to e
        } finally {
            flush()
        }
        return RoundResult(again, err, brake, data.base.values.count { !it.dir }, skipped, links, transferred)
    }

    private class Synced(val again: Boolean, val brake: Brake?, val skipped: List<Skipped>, val links: Int)

    private fun sync(allowDeletes: Boolean, restore: Boolean): Synced {
        pull()
        if (!fs.rootExists()) throw LocalMissingException()
        runCatching { fs.purgeTrash(opt.now() - opt.trashDays * 86_400_000L) }
        val sc = scan()
        val (v, skipped) = buildView(sc)
        val pl = filterForMode(makePlan(v), pair.mode)
        fetchHashes(pl)

        val files = data.base.values.count { !it.dir }
        val (ld, rd) = pl.deletions()
        val braked = !allowDeletes && !restore && ld + rd >= opt.brakeMin && (ld + rd) > opt.brakeRatio * files
        val brake = if (braked) Brake(ld, rd, files, opt.now()) else null

        val work = pl.ops.count { it.kind != OpKind.RECORD && it.kind != OpKind.FORGET && !(braked && it.kind.isDelete()) }
        setProgress(Progress(work, 0))
        var again = pl.again
        val conflictsBefore = conflicts
        val covered = wholeTrees(pl, sc)
        var firstErr: Exception? = null
        var errCount = 0
        for (o in pl.ops) {
            if (stopped()) throw StoppedException()
            val del = o.kind.isDelete()
            if (del && braked) continue
            if (del && restore) {
                // Restored instead: without a base, the side that still has the file sends it again.
                forget(o.key)
                again = true
                continue
            }
            if (o.key in covered) continue // trashed with its folder
            try {
                val kids = trees[o.key]
                if (kids != null && o.kind == OpKind.DELETE_LOCAL) deleteTree(o, kids) else exec(o)
            } catch (e: Skip) {
                again = true
            } catch (e: UnauthorizedException) {
                throw e
            } catch (e: StoppedException) {
                throw e
            } catch (e: Exception) {
                errCount++
                if (firstErr == null) firstErr = IOException("${o.key}: ${e.message ?: e.javaClass.simpleName}", e)
                opt.log("${o.kind.label} ${o.key}: $e")
            }
            if (o.kind != OpKind.RECORD && o.kind != OpKind.FORGET) setProgress(progress.copy(done = progress.done + 1, current = "", fraction = 0f))
        }
        if (conflicts != conflictsBefore && pair.mode == SyncMode.BOTH) again = true // the conflict copies go to the NAS at the next round
        firstErr?.let { e ->
            val msg = if (errCount > 1) "${e.message} (and ${errCount - 1} other errors)" else e.message ?: ""
            onEvent(SyncEvent("error", pair.id, msg = msg))
            throw IOException(msg, e)
        }
        return Synced(again, brake, (skipped + extraSkipped).take(200), sc.links)
    }

    private fun OpKind.isDelete() = this == OpKind.DELETE_LOCAL || this == OpKind.DELETE_REMOTE

    private fun setProgress(p: Progress) {
        progress = p
        onProgress(p)
    }

    private fun setCurrent(path: String, size: Long, done: Long) {
        if (stopped()) throw StoppedException()
        setProgress(progress.copy(current = path, fraction = if (size > 0) (done.toFloat() / size).coerceIn(0f, 1f) else 0f))
    }

    // ---- NAS changes ----

    /** Follows the change feed of the NAS into the remote tree. */
    private fun pull() {
        var cursor = data.cursor
        var fresh: HashMap<String, RemoteEntry>? = null
        val changes = ArrayList<Change>()
        while (true) {
            val c = remote.changes(pair.remote, cursor, 0)
            if (c.reset && fresh == null) fresh = HashMap()
            for (r in c.entries) {
                if (fresh != null) fresh[r.path] = r
                else {
                    data.remote[r.path] = r
                    changes += Change(Table.REMOTE, r.path, r)
                }
            }
            for (d in c.deleted) {
                if (fresh != null) fresh.remove(d)
                else {
                    data.remote.remove(d)
                    changes += Change(Table.REMOTE, d, null)
                }
            }
            cursor = c.cursor
            if (!c.more) break
        }
        if (fresh != null) {
            data.remote.clear()
            data.remote.putAll(fresh)
            data.cursor = cursor
            store.replaceRemote(pair.id, fresh, cursor)
            return
        }
        data.cursor = cursor
        store.write(pair.id, changes, cursor)
    }

    /** The cursor of the NAS change feed after the last round ("" before the first). */
    val cursor: String get() = data.cursor

    // ---- Phone folder ----

    private fun scan(): ScanResult {
        val res = ScanResult()
        fun walk(rel: String) {
            val list = fs.list(rel)
            if (list == null) {
                if (rel.isEmpty()) throw LocalMissingException()
                res.unreadable += n.key(rel)
                return
            }
            for (c in list) {
                val p = joinRel(rel, c.name)
                if (m.ignored(p)) continue
                if (c.link) {
                    res.links++
                    continue
                }
                if (c.other) continue // devices, sockets, pipes
                val prob = remoteNameProblem(c.name)
                if (prob.isNotEmpty()) {
                    res.skipped[p] = prob
                    continue
                }
                val e = if (c.dir) LocalEntry(p, true, id = c.id) else LocalEntry(p, false, c.size, c.mtime, c.id)
                res.entries[n.key(p)] = e
                if (c.dir) walk(p)
            }
        }
        walk("")
        return res
    }

    /** Keys the three trees, leaving out what is not synchronized. */
    private fun buildView(sc: ScanResult): Pair<View, List<Skipped>> {
        val skipped = sc.skipped.map { (p, why) -> Skipped(p, why) }.toMutableList()
        val remoteByKey = HashMap<String, RemoteEntry>()
        for (p in data.remote.keys.sorted()) {
            if (m.ignored(p)) continue
            val why = pathProblem(p, ::localNameProblem)
            if (why.isNotEmpty()) {
                skipped += Skipped(p, why)
                continue
            }
            val k = n.key(p)
            val o = remoteByKey[k]
            if (o != null) {
                skipped += Skipped(p, "another item on the NAS has the same name but for case: ${o.path}")
                continue
            }
            remoteByKey[k] = data.remote[p]!!
        }
        val base = HashMap<String, BaseEntry>()
        for ((k, b) in data.base.entries.toList()) {
            if (m.ignored(b.path)) {
                forget(k) // excluded since: neither side is touched
                continue
            }
            base[k] = b
        }
        skipped.sortBy { it.path }
        return View(sc.entries, remoteByKey, base, sc.unreadable) to skipped
    }

    /** Asks the NAS for the hashes the comparisons need. */
    private fun fetchHashes(pl: Plan) {
        val need = pl.ops.filter { it.kind == OpKind.COMPARE && it.r!!.hash.isEmpty() }.map { it.r!!.path }
        for (chunk in need.chunked(500)) {
            val got = runCatching { remote.hashes(pair.remote, chunk) }.getOrNull() ?: return
            for ((p, h) in got) data.remote[p]?.let { r ->
                r.hash = h
                queue(Change(Table.REMOTE, p, r))
            }
        }
    }

    /**
     * The phone folders deleted on the NAS with everything in them go to the
     * trash whole instead of file by file. Returns the ops covered by such a folder.
     */
    private fun wholeTrees(pl: Plan, sc: ScanResult): Set<String> {
        trees = HashMap()
        val del = HashMap<String, Op>()
        for (o in pl.ops) if (o.kind == OpKind.DELETE_LOCAL && o.b != null) del[o.key] = o
        val covered = HashSet<String>()
        // Shallowest first, so that a folder inside another is covered by it.
        val dirs = del.filter { it.value.b!!.dir }.keys.sortedBy { k -> k.count { it == '/' } }
        for (d in dirs) {
            if (d in covered) continue
            val kids = ArrayList<Op>()
            var whole = true
            for (k in sc.entries.keys) {
                if (!isUnder(k, d)) continue
                val o = del[k]
                if (o == null) {
                    whole = false // something here stays (new, or changed)
                    break
                }
                kids += o
            }
            if (!whole) continue
            trees[d] = kids
            for (o in kids) covered += o.key
        }
        return covered
    }

    /** Trashes a phone folder whose whole content was deleted on the NAS, after checking every file is still as synchronized. */
    private fun deleteTree(d: Op, kids: List<Op>) {
        for (o in kids) {
            val b = o.b!!
            if (b.dir) continue
            val cur = fs.stat(b.local())
            if (cur != null && (cur.dir || cur.size != b.size || cur.mtime != b.localMtime)) throw Skip() // changed meanwhile: decided file by file next round
        }
        if (!onlyJunkOrKnown(d.b!!.local(), kids)) throw Skip()
        fs.trash(d.b.local(), opt.now())
        for (o in kids) forget(o.key)
        forget(d.key)
    }

    // ---- State ----

    private fun queue(c: Change) {
        pending += c
        if (pending.size >= 200) flush()
    }

    private fun flush() {
        if (pending.isEmpty()) return
        runCatching { store.write(pair.id, pending.toList(), null) }.onSuccess { pending.clear() }
    }

    /** Stores that the item is the same on both sides: the phone item as it is now, and [r] on the NAS. */
    private fun record(key: String, localPath: String, r: RemoteEntry, hash: String) {
        val l = fs.stat(localPath) ?: throw Skip()
        val b = BaseEntry(
            path = r.path, localPath = if (localPath != r.path) localPath else "", dir = r.dir,
            size = if (l.dir) 0 else l.size, localMtime = if (l.dir) 0 else l.mtime, localId = l.id,
            version = r.version, remoteId = r.id, hash = hash,
        )
        data.base[key] = b
        queue(Change(Table.BASE, key, b))
    }

    private fun forget(key: String) {
        if (data.base.remove(key) != null) queue(Change(Table.BASE, key, null))
    }

    private fun setRemote(r: RemoteEntry) {
        data.remote[r.path] = r
        queue(Change(Table.REMOTE, r.path, r))
    }

    private fun dropRemote(p: String) {
        if (data.remote.remove(p) != null) queue(Change(Table.REMOTE, p, null))
    }

    /** Moves the base and remote entries below a moved folder. */
    private fun rebase(fromKey: String, toKey: String, fromRemote: String, toRemote: String, fromLocal: String, toLocal: String) {
        for ((k, b) in data.base.entries.toList()) {
            if (!isUnder(k, fromKey)) continue
            val nk = toKey + k.removePrefix(fromKey)
            var nb = b.copy(path = toRemote + b.path.removePrefix(fromRemote))
            if (b.localPath.isNotEmpty()) nb = nb.copy(localPath = toLocal + b.localPath.removePrefix(fromLocal))
            if (nb.localPath == nb.path) nb = nb.copy(localPath = "")
            data.base.remove(k)
            queue(Change(Table.BASE, k, null))
            data.base[nk] = nb
            queue(Change(Table.BASE, nk, nb))
        }
        for ((p, r) in data.remote.entries.toList()) {
            if (p.startsWith("$fromRemote/")) {
                dropRemote(p)
                setRemote(r.copy(path = toRemote + p.removePrefix(fromRemote)))
            }
        }
    }

    private fun sameLocal(want: LocalEntry): Boolean {
        val cur = fs.stat(want.path) ?: return false
        if (cur.dir != want.dir) return false
        return cur.dir || cur.size == want.size && cur.mtime == want.mtime
    }

    private fun fileHash(rel: String): String {
        val md = MessageDigest.getInstance("SHA-256")
        fs.openRead(rel, 0).use { s ->
            val buf = ByteArray(64 * 1024)
            while (true) {
                if (stopped()) throw StoppedException()
                val k = s.read(buf)
                if (k < 0) break
                md.update(buf, 0, k)
            }
        }
        return md.digest().toHex()
    }

    // ---- Ops ----

    private fun exec(o: Op) {
        when (o.kind) {
            OpKind.RECORD -> record(o.key, o.l!!.path, o.r!!, "")

            OpKind.FORGET -> forget(o.key)

            OpKind.MKDIR_LOCAL -> {
                val l = fs.stat(o.r!!.path)
                if (l != null && !l.dir) throw Skip()
                fs.mkdirs(o.r.path)
                record(o.key, o.r.path, o.r, "")
            }

            OpKind.MKDIR_REMOTE -> {
                val l = fs.stat(o.l!!.path)
                if (l == null || !l.dir) throw Skip()
                val r = remote.mkdir(pair.remote, o.l.path)
                setRemote(r)
                record(o.key, o.l.path, r, "")
            }

            OpKind.DOWNLOAD -> download(o.key, o.r!!, o.l)

            OpKind.UPLOAD -> {
                val ifMatch = if (o.b != null && !o.b.dir && o.r != null) o.b.version else ""
                upload(o.key, o.l!!, ifMatch)
            }

            OpKind.COMPARE -> {
                if (!sameLocal(o.l!!)) throw Skip()
                if (o.r!!.hash.isEmpty()) throw Skip() // the NAS did not give its hash yet
                val h = fileHash(o.l.path)
                if (h == o.r.hash) record(o.key, o.l.path, o.r, h) else conflict(o)
            }

            OpKind.CONFLICT -> conflict(o)

            OpKind.MOVE_REMOTE -> {
                // A phone rename: the item must still be at its new place, and nothing there on the NAS.
                if (!sameLocal(o.l2!!)) throw Skip()
                val b = o.b!!
                val r = remote.move(pair.remote, b.path, o.toRemote, b.version)
                dropRemote(b.path)
                setRemote(r)
                forget(o.key)
                if (b.dir) rebase(o.key, o.toKey, b.path, o.toRemote, b.local(), o.toLocal)
                record(o.toKey, o.toLocal, r, b.hash)
            }

            OpKind.MOVE_LOCAL -> {
                val b = o.b!!
                val l = o.l!!
                val from = b.local()
                if (!sameLocal(LocalEntry(from, l.dir, l.size, l.mtime))) throw Skip()
                val caseOnly = o.key == o.toKey
                if (!caseOnly && fs.stat(o.toLocal) != null) throw Skip() // something appeared there
                fs.move(from, o.toLocal)
                forget(o.key)
                if (b.dir) rebase(o.key, o.toKey, b.path, o.r2!!.path, from, o.toLocal)
                record(o.toKey, o.toLocal, o.r2!!, b.hash)
            }

            OpKind.DELETE_REMOTE -> {
                // Deleted here: still absent (or, for a type change, still the new type)?
                val b = o.b!!
                val l = fs.stat(b.local())
                if (l != null && l.dir == b.dir) throw Skip()
                try {
                    remote.delete(pair.remote, b.path, b.version)
                } catch (e: ConflictException) {
                    throw Skip() // changed on the NAS (or a folder with new content): it comes back with the next changes
                } catch (e: NotFoundException) {
                    // already gone
                }
                dropRemote(b.path)
                forget(o.key)
            }

            OpKind.DELETE_LOCAL -> {
                val b = o.b!!
                val p = b.local()
                val cur = fs.stat(p)
                if (cur == null) {
                    forget(o.key)
                    return
                }
                if (cur.dir != b.dir || !cur.dir && (cur.size != b.size || cur.mtime != b.localMtime)) throw Skip() // changed here meanwhile: it goes back to the NAS
                if (cur.dir && !onlyJunk(p)) throw Skip() // new content inside: kept (and sent back)
                fs.trash(p, opt.now())
                forget(o.key)
            }
        }
    }

    /** Whether a phone folder holds nothing synchronized. */
    private fun onlyJunk(rel: String): Boolean {
        val l = fs.list(rel) ?: return false
        return l.all { m.ignored(joinRel(rel, it.name)) }
    }

    /** Whether a phone folder holds only ignored files and the items of [kids] (checked from the storage: nothing new). */
    private fun onlyJunkOrKnown(rel: String, kids: List<Op>): Boolean {
        val known = kids.map { n.key(it.b!!.local()) }.toHashSet()
        fun walk(r: String): Boolean {
            val l = fs.list(r) ?: return false
            for (c in l) {
                val p = joinRel(r, c.name)
                if (m.ignored(p)) continue
                if (n.key(p) !in known) return false
                if (c.dir && !walk(p)) return false
            }
            return true
        }
        return walk(rel)
    }

    /**
     * Both sides changed. Two-way and NAS → phone: the phone version is
     * renamed "name (conflict from <phone> <date>).ext" (sent to the NAS at
     * the next round of a two-way pair) and the NAS version takes the name.
     * Phone → NAS: the phone stays as it is, its version goes to the NAS as
     * the conflict copy.
     */
    private fun conflict(o: Op) {
        val l = o.l!!
        val r = o.r!!
        if (!sameLocal(l)) throw Skip()
        if (pair.mode == SyncMode.UP) {
            if (l.dir || r.dir) {
                extraSkipped += Skipped(l.path, "a file and a folder have this name on the phone and on the NAS")
                return
            }
            val name = freeConflictName(l.path)
            val copy = remote.upload(pair.remote, UploadReq(name, l.size, l.mtime, "", { off -> fs.openRead(l.path, off) },
                progress = { setCurrent(l.path, l.size, it) }))
            transferred++
            setRemote(copy)
            conflicts++
            onEvent(SyncEvent("conflict", pair.id, l.path, name))
            record(o.key, l.path, r, "") // the NAS version stays the reference until the next change here
            return
        }
        val name = freeConflictName(l.path)
        fs.move(l.path, name)
        forget(o.key)
        onEvent(SyncEvent("conflict", pair.id, l.path, name))
        conflicts++
        if (r.dir) {
            fs.mkdirs(r.path)
            record(o.key, r.path, r, "")
            return
        }
        download(o.key, r, null)
    }

    private fun freeConflictName(rel: String): String {
        val dir = parentOf(rel)
        val name = rel.substringAfterLast('/')
        val now = opt.now()
        for (i in 1 until 100) {
            val c = joinRel(dir, conflictName(name, opt.device, now, opt.zone, i))
            val k = n.key(c)
            if (fs.stat(c) == null && data.remote.keys.none { n.key(it) == k }) return c
        }
        throw IOException("too many conflict copies")
    }

    /**
     * Fetches [r] into its phone place, which must still be what the plan saw
     * ([want] null: absent). The bytes go to a hidden part file in the same
     * folder, resumed after an interruption when the NAS version did not
     * change, then put in place.
     */
    private fun download(key: String, r: RemoteEntry, want: LocalEntry?) {
        fun check() {
            val cur = fs.stat(r.path)
            if (want == null && cur != null) throw Skip()
            if (want != null && (cur == null || cur.dir || cur.size != want.size || cur.mtime != want.mtime)) throw Skip()
        }
        check()
        val dir = parentOf(r.path)
        fs.mkdirs(dir)
        val part = joinRel(dir, partName(key + "\u0000" + r.version))
        var off = fs.stat(part)?.size ?: 0L
        if (off > r.size) {
            fs.delete(part)
            off = 0
        }
        setCurrent(r.path, r.size, off)
        val md = if (off == 0L && r.hash.isNotEmpty()) MessageDigest.getInstance("SHA-256") else null
        var got = 0L
        try {
            if (off < r.size || r.size == 0L) fs.openAppend(part).use { out ->
                val sink = object : OutputStream() {
                    var last = 0L
                    override fun write(b: Int) = write(byteArrayOf(b.toByte()), 0, 1)
                    override fun write(b: ByteArray, o: Int, len: Int) {
                        out.write(b, o, len)
                        md?.update(b, o, len)
                        got += len
                        val t = System.nanoTime()
                        if (t - last > 300_000_000) {
                            last = t
                            setCurrent(r.path, r.size, off + got)
                        }
                    }
                }
                remote.download(pair.remote, r.path, r.version, off, sink)
                out.flush()
                (out as? java.io.FileOutputStream)?.fd?.sync()
            }
        } catch (e: ChangedException) {
            fs.delete(part)
            throw Skip()
        }
        // Other errors leave the part, to resume.
        if (off + got != r.size) {
            fs.delete(part)
            throw IOException("incomplete download of ${r.path}")
        }
        if (md != null && md.digest().toHex() != r.hash) {
            fs.delete(part)
            throw IOException("checksum mismatch for ${r.path}")
        }
        fs.setMtime(part, r.mtime)
        check() // changed here meanwhile: the part is replanned as a conflict
        fs.replace(part, r.path)
        transferred++
        record(key, r.path, r, r.hash)
    }

    /**
     * Sends a phone file, replacing the NAS version [ifMatch] ("" = none
     * expected). When the NAS file changed meanwhile, the copy sent is kept
     * on the NAS under a conflict name.
     */
    private fun upload(key: String, l: LocalEntry, ifMatch: String) {
        if (!sameLocal(l)) throw Skip()
        val st = data.uploads[key]
        val resume = if (st != null && st.size == l.size && st.mtime == l.mtime && st.ifMatch == ifMatch) st.id else ""
        val req = UploadReq(l.path, l.size, l.mtime, ifMatch, { off -> fs.openRead(l.path, off) }, resume,
            onId = { id ->
                if (l.size > opt.resumeMin) { // sent in one request otherwise: nothing to resume
                    val u = UploadResume(id, l.size, l.mtime, ifMatch)
                    data.uploads[key] = u
                    runCatching { store.write(pair.id, listOf(Change(Table.UPLOADS, key, u)), null) }
                }
            },
            progress = { setCurrent(l.path, l.size, it) })
        setCurrent(l.path, l.size, 0)
        val r = try {
            remote.upload(pair.remote, req)
        } catch (c: ConflictException) {
            dropUpload(key)
            return uploadConflict(key, l, c)
        }
        dropUpload(key)
        transferred++
        setRemote(r)
        if (!sameLocal(l)) {
            // Changed during the upload: the base keeps the state sent, so the next round sends the new content.
            val b = BaseEntry(path = r.path, localPath = if (l.path != r.path) l.path else "", size = l.size, localMtime = l.mtime, localId = l.id, version = r.version, remoteId = r.id)
            data.base[key] = b
            queue(Change(Table.BASE, key, b))
            return
        }
        record(key, l.path, r, r.hash)
    }

    private fun dropUpload(key: String) {
        if (data.uploads.remove(key) != null) queue(Change(Table.UPLOADS, key, null))
    }

    /**
     * The NAS file changed while this one was sent. The copy sent becomes the
     * conflict copy; the NAS version keeps the name (and is downloaded at the
     * next round of a two-way pair).
     */
    private fun uploadConflict(key: String, l: LocalEntry, c: ConflictException) {
        if (c.current?.dir == true) throw Skip() // a folder took the name: planned again
        if (!sameLocal(l)) throw Skip()
        if (pair.mode == SyncMode.UP && c.current == null && c.uploadId.isNotEmpty()) {
            // Deleted on the NAS meanwhile: the phone version takes the name.
            val r = remote.commit(pair.remote, c.uploadId, l.path, "")
            transferred++
            setRemote(r)
            record(key, l.path, r, "")
            return
        }
        val name = freeConflictName(l.path)
        var r: RemoteEntry? = null
        if (c.uploadId.isNotEmpty()) r = remote.commit(pair.remote, c.uploadId, name, "")
        conflicts++
        onEvent(SyncEvent("conflict", pair.id, l.path, name))
        if (pair.mode == SyncMode.UP) {
            // The phone file stays; the NAS version is its reference until the next change here.
            r?.let { setRemote(it) }
            c.current?.let { cur -> setRemote(cur); record(key, l.path, cur, "") }
            return
        }
        fs.move(l.path, name)
        forget(key)
        if (r != null) {
            setRemote(r)
            runCatching { record(n.key(name), name, r, "") }
        }
        c.current?.let { setRemote(it) }
    }

    companion object {
        const val PART_PREFIX = ".strataos-sync-"

        fun partName(key: String): String {
            val h = MessageDigest.getInstance("SHA-256").digest(key.toByteArray())
            return PART_PREFIX + h.copyOf(8).toHex() + ".part"
        }
    }
}

fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
