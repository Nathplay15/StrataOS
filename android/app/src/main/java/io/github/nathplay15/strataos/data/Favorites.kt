package io.github.nathplay15.strataos.data

import android.content.Context
import androidx.core.content.edit
import java.io.File
import java.security.MessageDigest
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

/**
 * A favorite file or folder of the NAS. A favorite file may be kept
 * [offline]: a copy lives in the app's files (not the cache, which the system
 * may clear) and the sync worker refreshes it when the file changes.
 */
@Serializable
data class Favorite(
    val path: String,
    val name: String,
    val dir: Boolean = false,
    val offline: Boolean = false,
    val size: Long = 0, // of the offline copy, as the NAS reported it
    val modTime: String = "",
    val synced: Long = 0, // when the copy was last checked (ms)
    val missing: Boolean = false, // gone from the NAS: the copy stays until removed
)

/** What the sync must do for an offline favorite, given the NAS file. */
enum class SyncAction { DOWNLOAD, KEEP, MISSING }

/** Pure decision of the offline sync (unit tested). */
fun syncAction(f: Favorite, remoteSize: Long?, remoteModTime: String?, haveCopy: Boolean): SyncAction = when {
    remoteSize == null -> SyncAction.MISSING
    !haveCopy || remoteSize != f.size || remoteModTime != f.modTime -> SyncAction.DOWNLOAD
    else -> SyncAction.KEEP
}

class Favorites(private val ctx: Context) {
    private val p = ctx.getSharedPreferences("favorites", Context.MODE_PRIVATE)
    private val ser = ListSerializer(Favorite.serializer())
    private val json = Json { ignoreUnknownKeys = true }

    @Synchronized
    fun all(): List<Favorite> = p.getString("list", null)?.let { runCatching { json.decodeFromString(ser, it) }.getOrNull() } ?: emptyList()

    @Synchronized
    private fun save(list: List<Favorite>) = p.edit { putString("list", json.encodeToString(ser, list)) }

    fun get(path: String): Favorite? = all().firstOrNull { it.path == path }

    @Synchronized
    fun put(f: Favorite) = save(all().filter { it.path != f.path } + f)

    @Synchronized
    fun update(path: String, change: (Favorite) -> Favorite) = save(all().map { if (it.path == path) change(it) else it })

    @Synchronized
    fun remove(path: String) {
        copyOf(path).parentFile?.deleteRecursively()
        save(all().filter { it.path != path })
    }

    /** Where the offline copy of [path] lives (its own folder keeps the file name). */
    fun copyOf(path: String): File {
        val h = MessageDigest.getInstance("SHA-1").digest(path.toByteArray()).joinToString("") { "%02x".format(it) }
        return File(ctx.filesDir, "offline/$h/" + path.substringAfterLast('/'))
    }

    /** The offline copy of [path] if it matches the NAS file ([size], [modTime]) or when nothing newer is known. */
    fun usableCopy(path: String, size: Long? = null, modTime: String? = null): File? {
        val f = get(path) ?: return null
        val c = copyOf(path)
        if (!f.offline || !c.exists()) return null
        if (size != null && (size != f.size || modTime != f.modTime)) return null
        return c
    }

    fun clearAll() {
        File(ctx.filesDir, "offline").deleteRecursively()
        p.edit { clear() }
    }
}
