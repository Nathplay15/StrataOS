package io.github.nathplay15.strataos.sync

import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.io.RandomAccessFile
import java.nio.channels.Channels
import java.nio.file.Files
import java.nio.file.LinkOption
import java.nio.file.StandardCopyOption
import java.nio.file.attribute.BasicFileAttributes
import java.time.LocalDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

/** An item of a phone folder listing. [link]: a symbolic link; [other]: neither a file nor a folder. */
class LocalChild(val name: String, val dir: Boolean, val size: Long, val mtime: Long, val link: Boolean = false, val other: Boolean = false, val id: Long = 0)

/**
 * The phone side of a pair: a folder chosen with the system picker (Storage
 * Access Framework, [SafFs]) or a folder of the app ([FileFs]). Paths are
 * relative to the pair, with "/".
 */
interface LocalFs {
    /** Whether "A" and "a" are one name here. */
    val caseInsensitive: Boolean
    fun rootExists(): Boolean
    /** The items of a folder, or null when it cannot be read. */
    fun list(rel: String): List<LocalChild>?
    fun stat(rel: String): LocalEntry?
    fun mkdirs(rel: String)
    /** Renames or moves inside the pair, creating the folders above [to]; refuses to replace something. */
    fun move(from: String, to: String)
    fun openRead(rel: String, offset: Long): InputStream
    /** Opens a file for appending, creating it (download parts). */
    fun openAppend(rel: String): OutputStream
    /** Deletes a file (download parts only: user files go to [trash]). */
    fun delete(rel: String)
    /** Puts a finished download in place of [target] (which may exist). */
    fun replace(part: String, target: String)
    /** Sets the date of a file when the storage allows it. */
    fun setMtime(rel: String, ms: Long)
    /** Moves an item to the pair's trash, under a folder named after [stamp]. */
    fun trash(rel: String, stamp: Long)
    /** Deletes what has been in the trash since before [before] (ms). */
    fun purgeTrash(before: Long)
}

private val trashStamp = DateTimeFormatter.ofPattern("yyyy-MM-dd HH-mm-ss", Locale.ROOT)

/** The folder of the trash that receives what is deleted at [ms]. */
fun trashFolder(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String =
    TRASH_DIR + "/" + trashStamp.format(java.time.Instant.ofEpochMilli(ms).atZone(zone))

/** The time a trash folder was made, from its name (null: not ours). */
fun trashTime(name: String, zone: ZoneId = ZoneId.systemDefault()): Long? =
    runCatching { LocalDateTime.parse(name, trashStamp).atZone(zone).toInstant().toEpochMilli() }.getOrNull()

/** A folder of the phone reached with java.io (the app's own folder, and tests). */
class FileFs(private val root: File, fold: Boolean? = null) : LocalFs {
    override val caseInsensitive: Boolean by lazy { fold ?: probeCase(root) }

    private fun f(rel: String) = if (rel.isEmpty()) root else File(root, rel)

    override fun rootExists() = root.isDirectory

    override fun list(rel: String): List<LocalChild>? {
        val dir = f(rel)
        val names = dir.list() ?: return null
        return names.mapNotNull { n ->
            val p = File(dir, n).toPath()
            val a = runCatching { Files.readAttributes(p, BasicFileAttributes::class.java, LinkOption.NOFOLLOW_LINKS) }.getOrNull() ?: return@mapNotNull null
            LocalChild(n, a.isDirectory, if (a.isRegularFile) a.size() else 0, if (a.isRegularFile) a.lastModifiedTime().toMillis() else 0,
                link = a.isSymbolicLink, other = a.isOther, id = inode(a))
        }
    }

    override fun stat(rel: String): LocalEntry? {
        val a = runCatching { Files.readAttributes(f(rel).toPath(), BasicFileAttributes::class.java, LinkOption.NOFOLLOW_LINKS) }.getOrNull() ?: return null
        if (a.isSymbolicLink || a.isOther) return null
        return if (a.isDirectory) LocalEntry(rel, true, id = inode(a)) else LocalEntry(rel, false, a.size(), a.lastModifiedTime().toMillis(), inode(a))
    }

    private fun inode(a: BasicFileAttributes): Long = (a.fileKey()?.toString() ?: "").let { k ->
        // "(dev=…,ino=…)" on Linux.
        Regex("ino=(\\d+)").find(k)?.groupValues?.get(1)?.toLongOrNull() ?: 0L
    }

    override fun mkdirs(rel: String) {
        val d = f(rel)
        if (!d.isDirectory && !d.mkdirs()) throw IOException("cannot create $rel")
    }

    override fun move(from: String, to: String) {
        val dst = f(to)
        dst.parentFile?.mkdirs()
        val caseOnly = from.lowercase(Locale.ROOT) == to.lowercase(Locale.ROOT)
        if (!caseOnly && dst.exists()) throw IOException("$to exists")
        Files.move(f(from).toPath(), dst.toPath())
    }

    override fun openRead(rel: String, offset: Long): InputStream {
        val raf = RandomAccessFile(f(rel), "r")
        raf.seek(offset)
        return Channels.newInputStream(raf.channel)
    }

    override fun openAppend(rel: String): OutputStream = FileOutputStream(f(rel), true)

    override fun delete(rel: String) { f(rel).delete() }

    override fun replace(part: String, target: String) {
        Files.move(f(part).toPath(), f(target).toPath(), StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
    }

    override fun setMtime(rel: String, ms: Long) { f(rel).setLastModified(ms) }

    override fun trash(rel: String, stamp: Long) {
        val dst = File(root, trashFolder(stamp) + "/" + rel)
        dst.parentFile?.mkdirs()
        var d = dst
        var n = 2
        while (d.exists()) d = File(dst.parentFile, "${dst.name} ($n)").also { n++ }
        Files.move(f(rel).toPath(), d.toPath())
    }

    override fun purgeTrash(before: Long) {
        File(root, TRASH_DIR).listFiles()?.forEach { d -> trashTime(d.name)?.let { if (it < before) d.deleteRecursively() } }
    }

    companion object {
        /** Whether the file system of [dir] ignores case. */
        fun probeCase(dir: File): Boolean = runCatching {
            val f = File.createTempFile(".strataos-sync-case-", "", dir)
            try { File(dir, f.name.uppercase(Locale.ROOT)).exists() } finally { f.delete() }
        }.getOrDefault(true)
    }
}
