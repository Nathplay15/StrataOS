package io.github.nathplay15.strataos.sync

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import io.github.nathplay15.strataos.net.json
import kotlinx.serialization.Serializable

/** The state of a pair for the interface. state: idle, syncing, paused, error, brake. */
@Serializable
data class PairStatus(
    val state: String = "idle",
    val error: String = "",
    val lastSync: Long = 0,
    val files: Int = 0,
    val pending: Int = 0,
    val done: Int = 0,
    val current: String = "",
    val fraction: Float = 0f,
    val skipped: List<Skipped> = emptyList(),
    val links: Int = 0,
    val brake: Brake? = null,
    /** The user's answer to the brake ("apply" or "restore"), used by the next round. */
    val decision: String = "",
)

/** A conflict copy made by the sync. */
@Serializable
data class ConflictRecord(val path: String, val copy: String, val at: Long)

/**
 * Pairs, their status and conflicts, and the engine's state (base, remote
 * tree, cursor and uploads in progress) in one SQLite file, in the style of
 * BackupDb.
 */
class SyncDb(context: Context) : SQLiteOpenHelper(context, "sync.db", null, 1), SyncStore {
    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL("CREATE TABLE pairs (id TEXT PRIMARY KEY, pos INTEGER NOT NULL, json TEXT NOT NULL)")
        db.execSQL("CREATE TABLE status (id TEXT PRIMARY KEY, json TEXT NOT NULL)")
        db.execSQL("CREATE TABLE conflicts (pair TEXT NOT NULL, path TEXT NOT NULL, copy TEXT NOT NULL, at INTEGER NOT NULL)")
        db.execSQL("CREATE TABLE meta (pair TEXT NOT NULL, k TEXT NOT NULL, v TEXT NOT NULL, PRIMARY KEY (pair, k))")
        for (t in TABLES.values) db.execSQL("CREATE TABLE $t (pair TEXT NOT NULL, k TEXT NOT NULL, json TEXT NOT NULL, PRIMARY KEY (pair, k))")
    }

    override fun onUpgrade(db: SQLiteDatabase, old: Int, new: Int) = Unit

    // ---- Pairs ----

    fun pairs(): List<SyncPair> = readableDatabase.rawQuery("SELECT json FROM pairs ORDER BY pos, id", null).use { c ->
        buildList { while (c.moveToNext()) runCatching { json.decodeFromString(SyncPair.serializer(), c.getString(0)) }.getOrNull()?.let { add(it) } }
    }

    fun pair(id: String): SyncPair? = pairs().firstOrNull { it.id == id }

    fun savePair(p: SyncPair) {
        val db = writableDatabase
        val pos = db.rawQuery("SELECT pos FROM pairs WHERE id = ?", arrayOf(p.id)).use { c -> if (c.moveToFirst()) c.getInt(0) else null }
            ?: db.rawQuery("SELECT COALESCE(MAX(pos), 0) + 1 FROM pairs", null).use { c -> if (c.moveToFirst()) c.getInt(0) else 0 }
        db.insertWithOnConflict("pairs", null, ContentValues().apply {
            put("id", p.id); put("pos", pos); put("json", json.encodeToString(SyncPair.serializer(), p))
        }, SQLiteDatabase.CONFLICT_REPLACE)
    }

    /** Forgets a pair and its state (its files stay where they are). */
    fun removePair(id: String) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            db.delete("pairs", "id = ?", arrayOf(id))
            db.delete("status", "id = ?", arrayOf(id))
            db.delete("conflicts", "pair = ?", arrayOf(id))
            dropIn(db, id)
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    // ---- Status and conflicts ----

    fun status(id: String): PairStatus = readableDatabase.rawQuery("SELECT json FROM status WHERE id = ?", arrayOf(id)).use { c ->
        if (c.moveToFirst()) runCatching { json.decodeFromString(PairStatus.serializer(), c.getString(0)) }.getOrNull() else null
    } ?: PairStatus()

    @Synchronized
    fun updateStatus(id: String, f: (PairStatus) -> PairStatus): PairStatus {
        val s = f(status(id))
        writableDatabase.insertWithOnConflict("status", null, ContentValues().apply {
            put("id", id); put("json", json.encodeToString(PairStatus.serializer(), s))
        }, SQLiteDatabase.CONFLICT_REPLACE)
        return s
    }

    fun conflicts(id: String): List<ConflictRecord> =
        readableDatabase.rawQuery("SELECT path, copy, at FROM conflicts WHERE pair = ? ORDER BY at DESC LIMIT 500", arrayOf(id)).use { c ->
            buildList { while (c.moveToNext()) add(ConflictRecord(c.getString(0), c.getString(1), c.getLong(2))) }
        }

    fun addConflict(id: String, r: ConflictRecord) {
        writableDatabase.insert("conflicts", null, ContentValues().apply { put("pair", id); put("path", r.path); put("copy", r.copy); put("at", r.at) })
    }

    fun clearConflicts(id: String) {
        writableDatabase.delete("conflicts", "pair = ?", arrayOf(id))
    }

    /** The cursor of the NAS change feed after the last round ("" before the first). */
    fun cursor(id: String): String = readableDatabase.rawQuery("SELECT v FROM meta WHERE pair = ? AND k = 'cursor'", arrayOf(id)).use { c ->
        if (c.moveToFirst()) c.getString(0) else ""
    }

    // ---- SyncStore ----

    override fun load(pair: String): PairData {
        val d = PairData()
        val db = readableDatabase
        fun each(table: Table, f: (String, String) -> Unit) =
            db.rawQuery("SELECT k, json FROM ${TABLES[table]} WHERE pair = ?", arrayOf(pair)).use { c -> while (c.moveToNext()) f(c.getString(0), c.getString(1)) }
        each(Table.BASE) { k, v -> runCatching { json.decodeFromString(BaseEntry.serializer(), v) }.getOrNull()?.let { d.base[k] = it } }
        each(Table.REMOTE) { k, v -> runCatching { json.decodeFromString(RemoteEntry.serializer(), v) }.getOrNull()?.let { d.remote[k] = it } }
        each(Table.UPLOADS) { k, v -> runCatching { json.decodeFromString(UploadResume.serializer(), v) }.getOrNull()?.let { d.uploads[k] = it } }
        d.cursor = cursor(pair)
        return d
    }

    override fun write(pair: String, changes: List<Change>, cursor: String?) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            for (c in changes) {
                val t = TABLES[c.table]!!
                if (c.value == null) {
                    db.delete(t, "pair = ? AND k = ?", arrayOf(pair, c.key))
                    continue
                }
                val text = when (val v = c.value) {
                    is BaseEntry -> json.encodeToString(BaseEntry.serializer(), v)
                    is RemoteEntry -> json.encodeToString(RemoteEntry.serializer(), v)
                    is UploadResume -> json.encodeToString(UploadResume.serializer(), v)
                    else -> error("unexpected value")
                }
                db.insertWithOnConflict(t, null, ContentValues().apply { put("pair", pair); put("k", c.key); put("json", text) }, SQLiteDatabase.CONFLICT_REPLACE)
            }
            if (cursor != null) setCursor(db, pair, cursor)
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    override fun replaceRemote(pair: String, remote: Map<String, RemoteEntry>, cursor: String) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            db.delete(TABLES[Table.REMOTE]!!, "pair = ?", arrayOf(pair))
            for ((k, v) in remote) {
                db.insert(TABLES[Table.REMOTE]!!, null, ContentValues().apply { put("pair", pair); put("k", k); put("json", json.encodeToString(RemoteEntry.serializer(), v)) })
            }
            setCursor(db, pair, cursor)
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    override fun drop(pair: String) {
        val db = writableDatabase
        db.beginTransaction()
        try {
            dropIn(db, pair)
            db.setTransactionSuccessful()
        } finally {
            db.endTransaction()
        }
    }

    private fun dropIn(db: SQLiteDatabase, pair: String) {
        for (t in TABLES.values) db.delete(t, "pair = ?", arrayOf(pair))
        db.delete("meta", "pair = ?", arrayOf(pair))
    }

    private fun setCursor(db: SQLiteDatabase, pair: String, cursor: String) {
        db.insertWithOnConflict("meta", null, ContentValues().apply { put("pair", pair); put("k", "cursor"); put("v", cursor) }, SQLiteDatabase.CONFLICT_REPLACE)
    }

    companion object {
        private val TABLES = mapOf(Table.BASE to "base", Table.REMOTE to "remote", Table.UPLOADS to "uploads")
    }
}
