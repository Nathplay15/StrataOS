package io.github.nathplay15.strataos.ui.files

import io.github.nathplay15.strataos.net.FileEntry
import java.time.OffsetDateTime

enum class SortBy { NAME, DATE, SIZE, TYPE }

data class FileOrder(val by: SortBy = SortBy.NAME, val descending: Boolean = false)

private fun time(e: FileEntry): Long = runCatching { OffsetDateTime.parse(e.modTime).toInstant().toEpochMilli() }.getOrDefault(0L)

private fun ext(e: FileEntry): String = if (e.dir) "" else e.name.substringAfterLast('.', "").lowercase()

/** Folders first, then files, in the chosen order (names compare naturally: "2" before "10"). */
fun sortEntries(list: List<FileEntry>, o: FileOrder): List<FileEntry> {
    val byKey: Comparator<FileEntry> = when (o.by) {
        SortBy.NAME -> Comparator { a, b -> naturalCompare(a.name, b.name) }
        SortBy.DATE -> compareBy(::time)
        SortBy.SIZE -> compareBy { it.size }
        SortBy.TYPE -> compareBy(::ext)
    }
    val ordered = if (o.descending) byKey.reversed() else byKey
    val tie = Comparator<FileEntry> { a, b -> naturalCompare(a.name, b.name) }
    return list.sortedWith(compareBy<FileEntry> { !it.dir }.then(ordered).then(tie))
}

/** Case-insensitive comparison where digit runs compare as numbers. */
fun naturalCompare(a: String, b: String): Int {
    var i = 0
    var j = 0
    while (i < a.length && j < b.length) {
        val ca = a[i]
        val cb = b[j]
        if (ca.isDigit() && cb.isDigit()) {
            val si = i
            val sj = j
            while (i < a.length && a[i].isDigit()) i++
            while (j < b.length && b[j].isDigit()) j++
            val na = a.substring(si, i).trimStart('0')
            val nb = b.substring(sj, j).trimStart('0')
            if (na.length != nb.length) return na.length - nb.length
            val c = na.compareTo(nb)
            if (c != 0) return c
        } else {
            val c = ca.lowercaseChar().compareTo(cb.lowercaseChar())
            if (c != 0) return c
            i++
            j++
        }
    }
    return (a.length - i) - (b.length - j)
}

/** Where an item of a recycle bin came from: <share>/.recycle/<user>/<path> → <share>/<path>. */
fun recycleOrigin(path: String): String? {
    val i = path.indexOf("/.recycle/")
    if (i < 0) return null
    val rest = path.substring(i + "/.recycle/".length).substringAfter('/', "")
    if (rest.isEmpty()) return null
    return path.substring(0, i) + "/" + rest
}

/** A path shown to people: without the storage root. */
fun displayPath(path: String): String = path.removePrefix("/srv/strata").ifEmpty { "/" }
