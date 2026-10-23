package io.github.nathplay15.strataos.backup

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import io.github.nathplay15.strataos.upload.ResumeStore

/** What was backed up (by media key) and the uploads in progress. */
class BackupDb(context: Context) : SQLiteOpenHelper(context, "backup.db", null, 1), ResumeStore {
    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL("CREATE TABLE done (key TEXT PRIMARY KEY, path TEXT NOT NULL, at INTEGER NOT NULL)")
        db.execSQL("CREATE TABLE resume (key TEXT PRIMARY KEY, upload TEXT NOT NULL)")
    }

    override fun onUpgrade(db: SQLiteDatabase, old: Int, new: Int) = Unit

    fun doneKeys(): Set<String> = readableDatabase.rawQuery("SELECT key FROM done", null).use { c ->
        buildSet { while (c.moveToNext()) add(c.getString(0)) }
    }

    fun doneCount(): Int = readableDatabase.rawQuery("SELECT COUNT(*) FROM done", null).use { c -> if (c.moveToFirst()) c.getInt(0) else 0 }

    fun markDone(key: String, path: String) {
        writableDatabase.insertWithOnConflict("done", null, ContentValues().apply {
            put("key", key); put("path", path); put("at", System.currentTimeMillis())
        }, SQLiteDatabase.CONFLICT_REPLACE)
    }

    /** Backing up to another folder or NAS starts over (the dedupe check skips what is there). */
    fun reset() {
        writableDatabase.delete("done", null, null)
        writableDatabase.delete("resume", null, null)
    }

    override fun get(key: String): String? =
        readableDatabase.rawQuery("SELECT upload FROM resume WHERE key = ?", arrayOf(key)).use { c -> if (c.moveToFirst()) c.getString(0) else null }

    override fun put(key: String, uploadId: String) {
        writableDatabase.insertWithOnConflict("resume", null, ContentValues().apply { put("key", key); put("upload", uploadId) }, SQLiteDatabase.CONFLICT_REPLACE)
    }

    override fun remove(key: String) {
        writableDatabase.delete("resume", "key = ?", arrayOf(key))
    }
}
