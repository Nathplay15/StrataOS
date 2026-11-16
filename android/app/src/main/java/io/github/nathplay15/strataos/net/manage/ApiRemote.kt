package io.github.nathplay15.strataos.net.manage

import io.github.nathplay15.strataos.net.Api

import kotlinx.serialization.Serializable

// Published apps (api/h_remote.go, GET /api/remote/domains): the addresses of
// the installed apps, on the local network or under their published domain.

@Serializable data class PublishedDomain(val host: String = "", val kind: String = "", val app: String = "", val lanOnly: Boolean = false)
@Serializable data class PublishedApp(val stack: String = "", val name: String = "", val address: String = "", val status: String = "")
@Serializable data class RemoteState(val enabled: Boolean = false, val domains: List<PublishedDomain> = emptyList(), val apps: List<PublishedApp> = emptyList())
@Serializable data class PublishedLink(val name: String, val url: String)

fun Api.remoteState(): RemoteState = call(get("/api/remote/domains"), RemoteState.serializer())

/** The apps with a usable address: their https domain when published, their local address otherwise. */
fun publishedApps(s: RemoteState): List<PublishedLink> = s.apps.mapNotNull { a ->
    val d = s.domains.firstOrNull { it.kind == "app" && it.app == a.stack }
    val url = d?.host?.takeIf { it.isNotEmpty() }?.let { "https://$it" } ?: a.address.takeIf { it.isNotEmpty() }
    url?.let { PublishedLink(a.name.ifEmpty { a.stack }, it) }
}
