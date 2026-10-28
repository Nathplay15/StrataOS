package io.github.nathplay15.strataos.sync

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

/**
 * Turns paths into comparison keys: on case-insensitive storage (the shared
 * storage of Android and memory cards are) "Report.txt" and "report.txt"
 * are one file.
 */
class Namer(val fold: Boolean) {
    fun key(p: String): String = if (fold) p.lowercase(Locale.ROOT) else p
}

/** Names never synchronized: files of file browsers, office lock and temporary files. */
val DEFAULT_IGNORE = listOf(
    ".DS_Store", "Thumbs.db", "desktop.ini", "~$*", "*.tmp", ".~lock*", "._*", ".Spotlight-V100", ".Trashes", ".fseventsd",
    "\$RECYCLE.BIN", "System Volume Information", ".strataos-sync*", ".strata-upload-*", ".nomedia", ".pending-*", ".trashed-*",
)

/** The folder of a pair where deleted phone files wait 30 days (Android has no trash for these folders). */
const val TRASH_DIR = ".trash"

/**
 * Decides which paths are left out: ignore patterns (on the name, or on the
 * whole path when the pattern has a "/"), excluded subfolders and the trash.
 */
class Matcher(private val n: Namer, patterns: List<String>, exclude: List<String>) {
    private val patterns = patterns.map { it.trim() }.filter { it.isNotEmpty() }.map { if (n.fold) it.lowercase(Locale.ROOT) else it }
    private val exclude = exclude.map { it.trim('/') }.filter { it.isNotEmpty() }.map { n.key(it) }

    fun ignored(rel: String): Boolean {
        var name = rel.substringAfterLast('/')
        if (name.startsWith(".strataos-sync") || name.startsWith(".strata-upload-")) return true // our own temporary files
        val k = n.key(rel)
        if (k == TRASH_DIR || k.startsWith("$TRASH_DIR/")) return true
        var full = rel
        if (n.fold) {
            name = name.lowercase(Locale.ROOT)
            full = full.lowercase(Locale.ROOT)
        }
        for (p in patterns) if (globMatch(p, if ('/' in p) full else name)) return true
        return exclude.any { k == it || k.startsWith("$it/") }
    }
}

/** Shell-style matching of * (not across "/") and ?, as Go's path.Match for these patterns. */
fun globMatch(pattern: String, s: String): Boolean {
    fun m(pi: Int, si: Int): Boolean {
        if (pi == pattern.length) return si == s.length
        val c = pattern[pi]
        if (c == '*') {
            var j = si
            while (j <= s.length) {
                if (m(pi + 1, j)) return true
                if (j == s.length || s[j] == '/') return false
                j++
            }
            return false
        }
        if (si >= s.length) return false
        return if (c == '?') s[si] != '/' && m(pi + 1, si + 1) else s[si] == c && m(pi + 1, si + 1)
    }
    return m(0, 0)
}

/**
 * Why a NAS name cannot exist in a phone folder ("" = fine). Folders chosen
 * with the system picker go through the documents provider, which replaces
 * the characters memory cards (FAT, exFAT) refuse: such files are skipped
 * instead of being renamed behind the user's back.
 */
fun localNameProblem(name: String): String {
    if (!validUtf16(name)) return "invalid characters"
    if (name.any { it in "\"*:<>?\\|" }) return "the name contains a character the phone refuses (\" * : < > ? \\ |)"
    if (name.any { it.code < 0x20 || it.code == 0x7f }) return "the name contains a control character"
    if (name.toByteArray().size > 255) return "the name is too long"
    return ""
}

/** Why a phone name cannot be stored on the NAS ("" = fine). */
fun remoteNameProblem(name: String): String = when {
    !validUtf16(name) -> "invalid characters"
    '\\' in name -> "the NAS refuses names with \"\\\""
    name.toByteArray().size > 255 -> "the name is too long"
    name.startsWith(".strata-upload-") -> "reserved name"
    name.any { it.code < 0x20 || it.code == 0x7f } -> "the name contains a control character"
    else -> ""
}

private fun validUtf16(s: String): Boolean {
    var i = 0
    while (i < s.length) {
        val c = s[i]
        if (Character.isHighSurrogate(c)) {
            if (i + 1 >= s.length || !Character.isLowSurrogate(s[i + 1])) return false
            i += 2
            continue
        }
        if (Character.isLowSurrogate(c)) return false
        i++
    }
    return true
}

/** The problem of the first part of [rel] that [check] refuses. */
fun pathProblem(rel: String, check: (String) -> String): String {
    for (part in rel.split('/')) check(part).let { if (it.isNotEmpty()) return it }
    return ""
}

/** A device name usable inside a file name. */
fun safeDevice(d: String): String {
    var s = d.trim().map { if (it in "<>:\"/\\|?*" || it.code < 0x20) '-' else it }.joinToString("")
    if (s.isEmpty()) s = "phone"
    if (s.codePointCount(0, s.length) > 40) s = s.substring(0, s.offsetByCodePoints(0, 40))
    return s
}

private val conflictTime = DateTimeFormatter.ofPattern("yyyy-MM-dd HH-mm", Locale.ROOT)

/**
 * The copy kept when both sides changed a file:
 * "report (conflict from Pixel 8 2024-05-01 10-30).docx", n > 1 adding " n".
 */
fun conflictName(name: String, device: String, timeMs: Long, zone: ZoneId, n: Int): String {
    val dot = name.lastIndexOf('.')
    var ext = if (dot >= 0) name.substring(dot) else ""
    var base = name.removeSuffix(ext)
    if (base.isEmpty() || ' ' in ext || ext.length > 12) {
        base = name
        ext = "" // ".bashrc", or no real extension
    }
    val suffix = if (n > 1) " $n" else ""
    return "$base (conflict from ${safeDevice(device)} ${conflictTime.format(Instant.ofEpochMilli(timeMs).atZone(zone))}$suffix)$ext"
}

fun parentOf(p: String): String = p.substringBeforeLast('/', "")

fun joinRel(dir: String, name: String): String = if (dir.isEmpty()) name else "$dir/$name"

/** Whether key [k] is strictly below folder key [d]. */
fun isUnder(k: String, d: String): Boolean = k.startsWith("$d/")
