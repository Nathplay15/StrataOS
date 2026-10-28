package io.github.nathplay15.strataos.sync

/** What the state database holds for a pair. */
class PairData(
    val base: HashMap<String, BaseEntry> = HashMap(),
    val remote: HashMap<String, RemoteEntry> = HashMap(), // by NAS path
    var cursor: String = "",
    val uploads: HashMap<String, UploadResume> = HashMap(),
)

enum class Table { BASE, REMOTE, UPLOADS }

/** A pending write: a null value deletes the key. */
class Change(val table: Table, val key: String, val value: Any?)

/** The state of the pairs (SQLite in the app, memory in tests). */
interface SyncStore {
    fun load(pair: String): PairData
    /** Applies changes (and the cursor when not null) at once. */
    fun write(pair: String, changes: List<Change>, cursor: String?)
    /** Swaps the whole remote tree (after a reset of the feed). */
    fun replaceRemote(pair: String, remote: Map<String, RemoteEntry>, cursor: String)
    /** Forgets a pair. */
    fun drop(pair: String)
}
