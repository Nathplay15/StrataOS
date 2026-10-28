// Two-way folder sync between the phone and the NAS: a Kotlin port of the
// desktop sync engine (desktop/internal/engine), with the same semantics.
// Each pair keeps three views: the phone folder (scanned), the NAS folder
// (followed with the change feed of the Sync API) and the state both sides
// had at the last synchronization (the base). Comparing the three tells which
// side changed: a change on one side is copied to the other, changes on both
// sides keep both versions (a conflict copy), and deletions go to the NAS
// recycle bin or the pair's ".trash" folder on the phone. Nothing is
// overwritten or deleted without checking it is still what was planned.
package io.github.nathplay15.strataos.sync

import java.io.IOException
import java.io.InputStream
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** Directions of a pair. */
object SyncMode {
    const val BOTH = "both"
    const val UP = "up" // phone → NAS: changes of the NAS are not applied here
    const val DOWN = "down" // NAS → phone: changes made here are not sent
}

/** A phone folder synchronized with a NAS folder. */
@Serializable
data class SyncPair(
    val id: String,
    /** Absolute NAS folder (a shared folder or a folder inside one). */
    val remote: String,
    /** A Storage Access Framework tree URI, or "app:<name>" for a folder of the app. */
    val local: String,
    /** The phone folder as the user knows it. */
    val localLabel: String = "",
    val mode: String = SyncMode.BOTH,
    val wifiOnly: Boolean = true,
    val chargingOnly: Boolean = false,
    val paused: Boolean = false,
    /** Subfolders left out (slash paths below the pair). */
    val exclude: List<String> = emptyList(),
)

/** A file or folder of the NAS (mtime in Unix milliseconds). */
@Serializable
data class RemoteEntry(
    @SerialName("p") val path: String,
    @SerialName("d") val dir: Boolean = false,
    @SerialName("s") val size: Long = 0,
    @SerialName("m") val mtime: Long = 0,
    @SerialName("v") val version: String = "",
    @SerialName("h") var hash: String = "",
    @SerialName("i") val id: String = "",
)

/** A file or folder of the phone (mtime in Unix milliseconds; id: inode, 0 when unknown). */
data class LocalEntry(val path: String, val dir: Boolean, val size: Long = 0, val mtime: Long = 0, var id: Long = 0)

/** What both sides had at the last synchronization. */
@Serializable
data class BaseEntry(
    @SerialName("p") val path: String, // as named on the NAS
    @SerialName("l") val localPath: String = "", // as named here, when it differs (case)
    @SerialName("d") val dir: Boolean = false,
    @SerialName("s") val size: Long = 0,
    @SerialName("lm") val localMtime: Long = 0,
    @SerialName("li") val localId: Long = 0,
    @SerialName("v") val version: String = "",
    @SerialName("ri") val remoteId: String = "",
    @SerialName("h") val hash: String = "",
) {
    fun local(): String = localPath.ifEmpty { path }
}

/** An upload in progress, resumed when the file did not change. */
@Serializable
data class UploadResume(val id: String, @SerialName("s") val size: Long, @SerialName("m") val mtime: Long, @SerialName("v") val ifMatch: String)

/** A page of the NAS change feed. */
class Changes(val cursor: String, val reset: Boolean, val more: Boolean, val entries: List<RemoteEntry>, val deleted: List<String>)

/** A file to send: [open] reads it from an offset. */
class UploadReq(
    val rel: String,
    val size: Long,
    val mtime: Long,
    /** The NAS version replaced ("" = there must be none). */
    val ifMatch: String,
    val open: (Long) -> InputStream,
    val resumeId: String = "",
    val onId: (String) -> Unit = {},
    val progress: (Long) -> Unit = {},
)

/** The NAS as the engine sees it (the Sync API, or a fake in tests). */
interface Remote {
    fun changes(folder: String, cursor: String, waitSec: Int): Changes
    fun hashes(folder: String, paths: List<String>): Map<String, String>
    /** Writes the file from [offset] to [out]; throws [ChangedException] when it is no longer at [version]. Returns the bytes written. */
    fun download(folder: String, rel: String, version: String, offset: Long, out: java.io.OutputStream): Long
    /** Returns the new entry, or throws a [ConflictException] carrying the upload ID (kept by the NAS) when the file changed there. */
    fun upload(folder: String, u: UploadReq): RemoteEntry
    fun commit(folder: String, uploadId: String, rel: String, ifMatch: String): RemoteEntry
    fun mkdir(folder: String, rel: String): RemoteEntry
    fun move(folder: String, from: String, to: String, ifMatch: String): RemoteEntry
    fun delete(folder: String, rel: String, ifMatch: String)
}

/** The item changed on the NAS since the version the engine knew (412). */
class ConflictException(msg: String, val current: RemoteEntry? = null, var uploadId: String = "") : IOException("conflict: $msg")

/** The NAS folder of a pair is gone (nothing is deleted on the phone then). */
class NotFoundException(msg: String) : IOException(msg)

/** This phone was signed out of the NAS (its token was revoked). */
class UnauthorizedException : IOException("this phone is no longer paired with the NAS")

/** The file changed on the NAS during a download. */
class ChangedException : IOException("changed during the transfer")

/** The phone folder of a pair is missing or no longer allowed (permission revoked, card removed). */
class LocalMissingException : IOException("the phone folder is missing or no longer allowed")

/** The work was stopped (WorkManager constraints, the user). */
class StoppedException : IOException("stopped")

/** Something the user is told about: conflict, error, brake, signedout. */
data class SyncEvent(val kind: String, val pair: String, val path: String = "", val msg: String = "")

/** Deletions waiting for the user's decision (the deletion brake). */
@Serializable
data class Brake(val localDeletes: Int, val remoteDeletes: Int, val files: Int, val since: Long)

/** A path that is not synchronized, and why. */
@Serializable
data class Skipped(val path: String, val reason: String)
