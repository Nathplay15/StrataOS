package io.github.nathplay15.strataos.backup

/** A photo or video of the phone (MediaStore). */
data class MediaItem(
    val id: Long,
    val uri: String,
    val bucketId: String,
    val bucket: String,
    val name: String,
    val size: Long,
    val dateTaken: Long, // ms, 0 when unknown
    val dateModified: Long, // ms
    val video: Boolean,
) {
    /** Identifies this version of the file: an edited photo is backed up again. */
    val key get() = "$id:$size:$dateModified"

    /** The date the file gets on the NAS: when it was taken, else last modified. */
    val originalDate get() = if (dateTaken > 0) dateTaken else dateModified
}

data class BackupOptions(val buckets: Set<String>, val videos: Boolean, val destination: String)

/** Decides what the backup sends, and where. Pure logic (unit tested). */
object BackupPlanner {
    /** Each album goes to its own folder under the destination: <dest>/<album>/<file>. */
    fun folderFor(destination: String, item: MediaItem): String = destination.trimEnd('/') + "/" + safeName(item.bucket)

    /** A folder name the NAS accepts: no separators, control characters or leading dots. */
    fun safeName(s: String): String {
        val cleaned = s.map { c -> if (c == '/' || c == '\\' || c.code < 0x20 || c.code == 0x7f) '_' else c }
            .joinToString("").trim().trimStart('.').take(120).trim()
        return cleaned.ifEmpty { "Phone" }
    }

    /** What remains to send, newest first (recent photos are the ones not saved anywhere else yet). */
    fun pending(items: List<MediaItem>, done: Set<String>, o: BackupOptions): List<MediaItem> =
        items.asSequence()
            .filter { it.bucketId in o.buckets && (o.videos || !it.video) && it.size > 0 && it.key !in done && validName(it.name) }
            .sortedByDescending { it.originalDate }
            .toList()

    /** Names the NAS refuses (hidden files, separators) are left out rather than renamed. */
    fun validName(n: String) = n.isNotEmpty() && !n.startsWith(".") && n.none { it == '/' || it == '\\' || it.code < 0x20 } && n.toByteArray().size <= 255

    /** Groups by destination folder, keeping the order, in batches the dedupe check takes. */
    fun batches(items: List<MediaItem>, destination: String, max: Int = 500): List<Pair<String, List<MediaItem>>> =
        items.groupBy { folderFor(destination, it) }.flatMap { (folder, list) -> list.chunked(max).map { folder to it } }
}
