package io.github.nathplay15.strataos.net.manage

import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.net.Api

import kotlinx.serialization.Serializable
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

// Published apps (api/h_remote.go, GET /api/remote/domains): the addresses of
// the installed apps, on the local network or under their published domain.

@Serializable data class PublishedDomain(val host: String = "", val kind: String = "", val app: String = "", val lanOnly: Boolean = false)
@Serializable data class PublishedApp(val stack: String = "", val name: String = "", val address: String = "", val status: String = "", val icon: String = "")
@Serializable data class RemoteState(val enabled: Boolean = false, val domains: List<PublishedDomain> = emptyList(), val apps: List<PublishedApp> = emptyList())
@Serializable data class PublishedLink(val name: String, val url: String, val icon: String = "")

fun Api.remoteState(): RemoteState = call(get("/api/remote/domains"), RemoteState.serializer())

/** Rewrites a loopback address of the NAS (127.0.0.1/localhost only works on the NAS
 *  itself) to the host this app is connected to, so the launcher opens the app. */
fun onServerHost(url: String): String {
    val u = url.toHttpUrlOrNull() ?: return url
    val h = u.host
    if (h != "127.0.0.1" && h != "localhost" && h != "::1") return url
    val server = Graph.prefs.server?.url?.toHttpUrlOrNull()?.host ?: return url
    return u.newBuilder().host(server).build().toString()
}

/** The apps with a usable address: their https domain when published, their local address otherwise. */
fun publishedApps(s: RemoteState): List<PublishedLink> = s.apps.mapNotNull { a ->
    val d = s.domains.firstOrNull { it.kind == "app" && it.app == a.stack }
    val url = d?.host?.takeIf { it.isNotEmpty() }?.let { "https://$it" } ?: a.address.takeIf { it.isNotEmpty() }
    url?.let { PublishedLink(a.name.ifEmpty { a.stack }, onServerHost(it), a.icon) }
}

/**
 * The bitmap-friendly variant of an icon URL, null when none: the dashboard
 * icon CDN also serves png (Android does not decode svg), other svg addresses
 * are kept only when they may be raster.
 */
fun rasterIconUrl(icon: String): String? {
    val u = icon.toHttpUrlOrNull() ?: return null
    val p = u.encodedPath
    return when {
        u.host == "cdn.jsdelivr.net" && p.startsWith("/gh/homarr-labs/dashboard-icons/svg/") ->
            u.newBuilder().encodedPath(p.replaceFirst("/svg/", "/png/").replace(".svg", ".png")).build().toString()
        p.endsWith(".svg") -> null
        else -> icon
    }
}
