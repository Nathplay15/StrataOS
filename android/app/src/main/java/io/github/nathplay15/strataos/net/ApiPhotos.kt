package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject
import okhttp3.HttpUrl

// The Photos app of the NAS (api/h_photos.go, h_albums.go).

@Serializable data class Photo(val path: String = "", val date: Long = 0, val mtime: Long = 0, val size: Long = 0, val video: Boolean = false) {
    val name get() = path.substringAfterLast('/')
}
@Serializable data class PhotoPage(
    val installed: Boolean = true, val total: Int = 0, val items: List<Photo> = emptyList(), val running: Boolean = false,
    val folders: List<String>? = null,
)
@Serializable data class Album(val id: String = "", val name: String = "", val count: Int = 0, val cover: String = "", val created: String = "", val shared: Boolean = false)

@Serializable private data class NewAlbum(val name: String, val items: List<String>)
@Serializable private data class AlbumChange(val name: String = "", val add: List<String> = emptyList(), val remove: List<String> = emptyList())
@Serializable private data class AlbumId(val id: String = "")

/** A page of the timeline (newest first), of an album, or of a search. */
fun Api.photos(offset: Int, limit: Int, album: String = "", q: String = ""): PhotoPage {
    val query = buildList {
        add("offset" to offset.toString()); add("limit" to limit.toString())
        if (album.isNotEmpty()) add("album" to album)
        if (q.isNotBlank()) add("q" to q.trim())
    }
    return call(get("/api/photos", *query.toTypedArray()), PhotoPage.serializer())
}

/** Thumbnail of an indexed photo or video: "s" (360 px) or "l" (1600 px). */
fun Api.photoThumbUrl(path: String, size: String): HttpUrl = url("/api/photos/thumb", "path" to path, "size" to size)

fun Api.albums(): List<Album> = call(get("/api/photos/albums"), ListSerializer(Album.serializer()))
fun Api.createAlbum(name: String, items: List<String>): String =
    call(request(url("/api/photos/albums")).post(body(NewAlbum(name, items))).build(), AlbumId.serializer()).id
fun Api.changeAlbum(id: String, name: String = "", add: List<String> = emptyList(), remove: List<String> = emptyList()) {
    call(request(url("/api/photos/albums/$id")).put(body(AlbumChange(name, add, remove))).build(), JsonObject.serializer())
}
fun Api.deleteAlbum(id: String) {
    call(request(url("/api/photos/albums/$id")).delete().build(), JsonObject.serializer())
}
