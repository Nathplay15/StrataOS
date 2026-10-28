package io.github.nathplay15.strataos.sync

// The planner, as desktop/internal/engine/plan.go: what to do for every path
// from the phone, NAS and base views.

enum class OpKind(val label: String) {
    MOVE_REMOTE("move-remote"), // a phone rename or move, done on the NAS
    MOVE_LOCAL("move-local"), // a rename or move on the NAS, done here
    MKDIR_REMOTE("mkdir-remote"),
    MKDIR_LOCAL("mkdir-local"),
    COMPARE("compare"), // both sides have a file of the same size: same content?
    CONFLICT("conflict"), // both changed: keep both versions
    UPLOAD("upload"),
    DOWNLOAD("download"),
    RECORD("record"), // already the same on both sides
    FORGET("forget"), // gone on both sides
    DELETE_REMOTE("delete-remote"), // deleted here
    DELETE_LOCAL("delete-local"), // deleted on the NAS
}

class Op(
    val kind: OpKind,
    val key: String,
    val l: LocalEntry? = null,
    val r: RemoteEntry? = null,
    val b: BaseEntry? = null,
    // Moves: the destination (key, local path, remote path) and the entries there.
    val toKey: String = "",
    val toLocal: String = "",
    val toRemote: String = "",
    val l2: LocalEntry? = null,
    val r2: RemoteEntry? = null,
) {
    override fun toString() = "${kind.label}:$key"
}

/** What the planner compares, by key. Unreadable phone folders: nothing below them is decided. */
class View(
    val local: Map<String, LocalEntry>,
    val remote: Map<String, RemoteEntry>,
    val base: Map<String, BaseEntry>,
    val unreadable: List<String> = emptyList(),
    val localId: (LocalEntry) -> Long = { it.id },
)

class Plan(val ops: MutableList<Op> = mutableListOf(), var again: Boolean = false) {
    /** Files deleted on each side: (here, on the NAS). */
    fun deletions(): Pair<Int, Int> {
        var local = 0
        var remote = 0
        for (o in ops) {
            if (o.kind == OpKind.DELETE_LOCAL && o.b != null && !o.b.dir) local++
            if (o.kind == OpKind.DELETE_REMOTE && o.b != null && !o.b.dir) remote++
        }
        return local to remote
    }
}

fun localChanged(l: LocalEntry?, b: BaseEntry?): Boolean = when {
    b == null -> l != null
    l == null || l.dir != b.dir -> true
    l.dir -> false
    else -> l.size != b.size || l.mtime != b.localMtime
}

fun remoteChanged(r: RemoteEntry?, b: BaseEntry?): Boolean = when {
    b == null -> r != null
    r == null || r.dir != b.dir -> true
    r.dir -> false
    else -> r.version != b.version
}

private fun depth(k: String) = k.count { it == '/' }

/** Decides what to do for every path. */
fun makePlan(v: View): Plan {
    val p = Plan()
    val keys = HashSet<String>().apply { addAll(v.local.keys); addAll(v.remote.keys); addAll(v.base.keys) }
    val done = HashSet<String>()
    for (k in keys) for (u in v.unreadable) if (k == u || isUnder(k, u)) done += k
    // Skip a moved folder's content this round: it is compared again once the base follows the move.
    fun settle(from: String, to: String) {
        for (k in keys) if (isUnder(k, from) || isUnder(k, to)) done += k
        p.again = true
    }
    val sorted = keys.sorted()

    // Phone moves: a synced item gone, a new item with its file ID (and, for
    // files, its size and date), nothing on the NAS at the new place. Only
    // storage that gives file IDs (the app's own folder) has them.
    val gone = v.base.any { (k, b) -> v.local[k] == null && b.localId != 0L && k !in done }
    val newLocal = HashMap<Long, String>()
    if (gone) for (k in keys) {
        val l = v.local[k] ?: continue
        if (v.base[k] == null && v.remote[k] == null && k !in done) v.localId(l).takeIf { it != 0L }?.let { newLocal[it] = k }
    }
    for (k in sorted) {
        val l = v.local[k]
        val r = v.remote[k]
        val b = v.base[k]
        if (k in done || b == null || l != null || r == null || remoteChanged(r, b) || b.localId == 0L) continue
        val k2 = newLocal[b.localId] ?: continue
        if (k2 in done) continue
        val l2 = v.local[k2]!!
        if (l2.dir != b.dir || !b.dir && (l2.size != b.size || l2.mtime != b.localMtime)) continue
        p.ops += Op(OpKind.MOVE_REMOTE, k, b = b, r = r, toKey = k2, toLocal = l2.path, toRemote = l2.path, l2 = l2)
        done += k
        done += k2
        newLocal.remove(b.localId)
        if (b.dir) settle(k, k2)
    }
    // Moves on the NAS: a synced item gone there, a new one with its ID (and,
    // for files, its version: same inode, size and date).
    val newRemote = HashMap<String, String>()
    for (k in keys) {
        val r = v.remote[k] ?: continue
        if (v.base[k] == null && v.local[k] == null && r.id.isNotEmpty() && k !in done) newRemote[r.id] = k
    }
    for (k in sorted) {
        val l = v.local[k]
        val r = v.remote[k]
        val b = v.base[k]
        if (k in done || b == null || r != null || l == null || localChanged(l, b) || b.remoteId.isEmpty()) continue
        val k2 = newRemote[b.remoteId] ?: continue
        if (k2 in done) continue
        val r2 = v.remote[k2]!!
        if (r2.dir != b.dir || !b.dir && r2.version != b.version) continue
        p.ops += Op(OpKind.MOVE_LOCAL, k, b = b, l = l, toKey = k2, toLocal = r2.path, toRemote = r2.path, r2 = r2)
        done += k
        done += k2
        newRemote.remove(b.remoteId)
        if (b.dir) settle(k, k2)
    }

    for (k in sorted) {
        if (k in done) continue
        val l = v.local[k]
        val r = v.remote[k]
        val b = v.base[k]
        val lc = localChanged(l, b)
        val rc = remoteChanged(r, b)
        fun add(kind: OpKind) { p.ops += Op(kind, k, l = l, r = r, b = b) }
        when {
            !lc && !rc -> {
                if (l == null || r == null || b == null) continue
                // Same key, another spelling: a change of case on one side.
                if (r.path != b.path) p.ops += Op(OpKind.MOVE_LOCAL, k, l = l, b = b, toKey = k, toLocal = r.path, toRemote = r.path, r2 = r)
                else if (l.path != b.local()) p.ops += Op(OpKind.MOVE_REMOTE, k, r = r, b = b, toKey = k, toLocal = l.path, toRemote = l.path, l2 = l)
            }
            lc && !rc -> when {
                l == null -> add(OpKind.DELETE_REMOTE) // folders after their content (deepest first)
                b != null && l.dir != b.dir -> { add(OpKind.DELETE_REMOTE); p.again = true } // the type changed here: remove the old one first
                l.dir -> add(OpKind.MKDIR_REMOTE)
                else -> add(OpKind.UPLOAD)
            }
            !lc && rc -> when {
                r == null -> add(OpKind.DELETE_LOCAL)
                b != null && r.dir != b.dir -> { add(OpKind.DELETE_LOCAL); p.again = true }
                r.dir -> add(OpKind.MKDIR_LOCAL)
                else -> add(OpKind.DOWNLOAD)
            }
            else -> when { // changed on both sides
                l == null && r == null -> add(OpKind.FORGET)
                l == null -> add(if (r!!.dir) OpKind.MKDIR_LOCAL else OpKind.DOWNLOAD) // deleted here, changed there: the NAS version comes back
                r == null -> add(if (l.dir) OpKind.MKDIR_REMOTE else OpKind.UPLOAD) // changed here, deleted there: the phone version goes back
                l.dir && r.dir -> add(OpKind.RECORD)
                l.dir != r.dir -> add(OpKind.CONFLICT)
                l.size == r.size -> add(OpKind.COMPARE)
                else -> add(OpKind.CONFLICT)
            }
        }
    }
    fun prio(o: Op) = when (o.kind) {
        OpKind.MOVE_REMOTE, OpKind.MOVE_LOCAL -> 0
        OpKind.MKDIR_REMOTE, OpKind.MKDIR_LOCAL -> 1
        OpKind.COMPARE, OpKind.CONFLICT -> 2
        OpKind.UPLOAD, OpKind.DOWNLOAD -> 3
        OpKind.RECORD, OpKind.FORGET -> 4
        else -> 5
    }
    p.ops.sortWith { a, b ->
        val pa = prio(a)
        val pb = prio(b)
        when {
            pa != pb -> pa.compareTo(pb)
            pa == 5 && depth(a.key) != depth(b.key) -> depth(b.key).compareTo(depth(a.key)) // deletions: content before folders
            depth(a.key) != depth(b.key) -> depth(a.key).compareTo(depth(b.key))
            else -> a.key.compareTo(b.key)
        }
    }
    return p
}

/**
 * One-way pairs keep the planner and drop what goes the other way: a
 * phone → NAS pair never changes the phone (a conflict keeps the NAS version
 * and sends the phone one as a conflict copy), a NAS → phone pair never
 * changes the NAS (the phone's conflict copy stays on the phone).
 */
fun filterForMode(p: Plan, mode: String): Plan {
    val drop = when (mode) {
        SyncMode.UP -> setOf(OpKind.MOVE_LOCAL, OpKind.MKDIR_LOCAL, OpKind.DOWNLOAD, OpKind.DELETE_LOCAL)
        SyncMode.DOWN -> setOf(OpKind.MOVE_REMOTE, OpKind.MKDIR_REMOTE, OpKind.UPLOAD, OpKind.DELETE_REMOTE)
        else -> emptySet()
    }
    if (drop.isEmpty()) return p
    return Plan(p.ops.filterNot { it.kind in drop }.toMutableList(), p.again)
}
