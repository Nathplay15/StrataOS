package io.github.nathplay15.strataos.update

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * A StrataOS version as VERSION, tags and file names write it: 0.10.1, and
 * 0.11.0~beta.1 (VERSION) or 0.11.0-beta.1 (tag v0.11.0-beta.1, file names)
 * for a pre-release, which sorts before the final version (alpha < beta < rc).
 * Same rules as backend/internal/release.
 */
data class AppVersion(val major: Int, val minor: Int, val patch: Int, val pre: String = "", val preNum: Int = 0) : Comparable<AppVersion> {
    val prerelease get() = pre.isNotEmpty()

    /** The versionCode of the app (android/app/build.gradle.kts): pre-releases share it with the final version. */
    val versionCode get() = major * 1_000_000L + minor * 1_000L + patch

    /** As tags and file names write it (strataos-0.11.0-beta.1.apk). */
    val fileVersion get() = "$major.$minor.$patch" + if (prerelease) "-$pre.$preNum" else ""

    override fun toString() = "$major.$minor.$patch" + if (prerelease) "~$pre.$preNum" else ""

    override fun compareTo(other: AppVersion): Int = compareValuesBy(this, other, { it.major }, { it.minor }, { it.patch }, { preRank(it.pre) }, { it.preNum })

    companion object {
        private val re = Regex("""^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:[~-](alpha|beta|rc)\.([1-9][0-9]*))?$""")

        private fun preRank(p: String) = when (p) {
            "alpha" -> 0
            "beta" -> 1
            "rc" -> 2
            else -> 3 // final release
        }

        /** Parses a version, a tag (v0.11.0-beta.1) or a versionName; null when it is none of these. */
        fun parse(s: String): AppVersion? {
            val m = re.matchEntire(s.trim()) ?: return null
            val (a, b, c, pre, n) = m.destructured
            return runCatching { AppVersion(a.toInt(), b.toInt(), c.toInt(), pre, n.toIntOrNull() ?: 0) }.getOrNull()
        }

        /** The version a versionCode stands for (without its pre-release part, which the code does not hold). */
        fun fromVersionCode(code: Long): AppVersion = AppVersion((code / 1_000_000).toInt(), (code / 1_000 % 1_000).toInt(), (code % 1_000).toInt())
    }
}

/** A release as the GitHub API returns it (only what the app uses). */
@Serializable
data class Release(
    @SerialName("tag_name") val tag: String = "",
    val name: String = "",
    val body: String = "",
    val draft: Boolean = false,
    val prerelease: Boolean = false,
    @SerialName("html_url") val htmlUrl: String = "",
    @SerialName("published_at") val publishedAt: String = "",
    val assets: List<Asset> = emptyList(),
) {
    val version get() = AppVersion.parse(tag)
}

@Serializable
data class Asset(
    val name: String = "",
    @SerialName("browser_download_url") val url: String = "",
    val size: Long = 0,
)

/** The files of a release the app needs: its APK and the checksums. */
data class ReleaseFiles(val apk: Asset, val sums: Asset)

object Releases {
    const val SUMS = "SHA256SUMS"

    /** The newest usable release: no drafts, pre-releases only with [beta], and a valid tag. */
    fun pick(releases: List<Release>, beta: Boolean): Release? =
        releases.filter { !it.draft && (beta || !it.prerelease) && it.version != null && (beta || it.version?.prerelease == false) }
            .maxByOrNull { it.version!! }

    /** The APK of a release (strataos-<file version>.apk) and its SHA256SUMS, or null when one is missing. */
    fun files(r: Release): ReleaseFiles? {
        val v = r.version ?: return null
        val apk = r.assets.firstOrNull { it.name == "strataos-${v.fileVersion}.apk" } ?: return null
        val sums = r.assets.firstOrNull { it.name == SUMS } ?: return null
        return ReleaseFiles(apk, sums)
    }

    /** Whether [latest] is worth installing over [installed]. */
    fun isNewer(latest: AppVersion, installed: AppVersion) = latest > installed

    /**
     * Reads a SHA256SUMS file ("<hex>  <name>", or "<hex> *<name>" in binary
     * mode): file name → lower case hex digest. Malformed lines are skipped.
     */
    fun parseSums(text: String): Map<String, String> {
        val re = Regex("""^([0-9a-fA-F]{64}) [ *](.+)$""")
        return text.lineSequence().mapNotNull { re.matchEntire(it.trimEnd('\r')) }
            .associate { m -> m.groupValues[2].trim() to m.groupValues[1].lowercase() }
    }

    /**
     * Whether a downloaded APK is signed like the installed app: the same
     * signing certificates (SHA-256 digests), or, after a key rotation, a
     * signing history ([archiveHistory]) that holds the installed certificate.
     */
    fun sameSigner(installed: Set<String>, archive: Set<String>, archiveHistory: List<String> = emptyList()): Boolean =
        installed.isNotEmpty() && archive.isNotEmpty() && (installed == archive || installed.size == 1 && installed.first() in archiveHistory)

    /** Release notes (GitHub Markdown) as plain text: no heading marks, bold, code marks or link targets. */
    fun plainNotes(body: String): String = body.replace("\r\n", "\n").lineSequence().joinToString("\n") { line ->
        line.replace(Regex("""^#{1,6}\s+"""), "")
            .replace(Regex("""\[([^\]]+)]\([^)]+\)"""), "$1")
            .replace("**", "").replace("`", "")
    }.replace(Regex("\n{3,}"), "\n\n").trim()
}
