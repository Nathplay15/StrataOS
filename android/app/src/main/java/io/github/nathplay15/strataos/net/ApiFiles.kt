package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject
import okhttp3.HttpUrl

// File manager v2: search, copy/move, recycle bin, properties, share links.

@Serializable data class FileInfo(
    val name: String = "", val path: String = "", val size: Long = 0, val mode: String = "", val modTime: String = "",
    val dir: Boolean = false, val link: Boolean = false, val writable: Boolean = false, val archive: String = "",
    val owner: String = "", val group: String = "", val mime: String = "", val changed: String = "",
)
@Serializable data class SearchResult(val path: String = "", val entries: List<FileEntry> = emptyList(), val truncated: Boolean = false)
@Serializable data class TrashResult(val recycled: Int = 0, val deleted: Int = 0)
@Serializable data class ShareLink(
    val id: String = "", val owner: String = "", val path: String = "", val album: String = "", val dir: Boolean = false,
    val download: Boolean = false, val upload: Boolean = false, val password: Boolean = false, val expires: String = "",
    val maxDownloads: Int = 0, val downloads: Int = 0, val uploads: Int = 0, val created: String = "", val lastUsed: String = "", val name: String = "",
)
@Serializable data class NewLink(
    val path: String = "", val album: String = "", val download: Boolean = true, val upload: Boolean = false,
    val password: String = "", val expiresDays: Int = 0, val maxDownloads: Int = 0,
)
/** A shared folder as GET /api/shares lists it (the fields the file browser needs). */
@Serializable data class ShareSummary(val name: String = "", val path: String = "", val recycleBin: Boolean = false)

@Serializable private data class CopyBody(val paths: List<String>, val to: String, val move: Boolean)
@Serializable private data class PathsBody(val paths: List<String>)
@Serializable private data class RenameBody(val from: String, val to: String)

fun Api.listFiles(path: String, hidden: Boolean = false): FileList =
    call(if (hidden) get("/api/files", "path" to path, "hidden" to "1") else get("/api/files", "path" to path), FileList.serializer())

fun Api.searchFiles(path: String, q: String): SearchResult = call(get("/api/files/search", "path" to path, "q" to q), SearchResult.serializer())

/** Copies or moves items into a folder (a job on the NAS). */
fun Api.copyFiles(paths: List<String>, to: String, move: Boolean): Job =
    call(request(url("/api/files/copy")).post(body(CopyBody(paths, to, move))).build(), Job.serializer())

/** Deletes through the recycle bin of the share when it has one. */
fun Api.trashFiles(paths: List<String>): TrashResult =
    call(request(url("/api/files/trash")).post(body(PathsBody(paths))).build(), TrashResult.serializer())

fun Api.renameFile(from: String, to: String) {
    call(request(url("/api/files/rename")).post(body(RenameBody(from, to))).build(), JsonObject.serializer())
}

fun Api.fileInfo(path: String): FileInfo = call(get("/api/files/info", "path" to path), FileInfo.serializer())

fun Api.shares(): List<ShareSummary> = call(get("/api/shares"), ListSerializer(ShareSummary.serializer()))

fun Api.shareLinks(): List<ShareLink> = call(get("/api/links"), ListSerializer(ShareLink.serializer()))
fun Api.createShareLink(l: NewLink): ShareLink = call(request(url("/api/links")).post(body(l)).build(), ShareLink.serializer())
fun Api.deleteShareLink(id: String) {
    call(request(url("/api/links/$id")).delete().build(), JsonObject.serializer())
}

/** The public address of a share link (what the web interface copies). */
fun Api.linkUrl(id: String): String = base.newBuilder().encodedPath("/s/$id").build().toString()

/** A preview of an image file, at most [px] pixels (the NAS keeps it cached). */
fun Api.imageUrl(path: String, px: Int): HttpUrl = url("/api/files/image", "path" to path, "size" to px.toString())
