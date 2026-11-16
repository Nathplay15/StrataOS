package io.github.nathplay15.strataos.manage

import io.github.nathplay15.strataos.net.json
import io.github.nathplay15.strataos.net.manage.PublishedApp
import io.github.nathplay15.strataos.net.manage.PublishedDomain
import io.github.nathplay15.strataos.net.manage.PublishedLink
import io.github.nathplay15.strataos.net.manage.RemoteState
import io.github.nathplay15.strataos.net.manage.publishedApps
import io.github.nathplay15.strataos.net.manage.rasterIconUrl
import org.junit.Assert.assertEquals
import org.junit.Test

class RemoteTest {
    @Test
    fun parsesTheDomainsResponse() {
        val s = json.decodeFromString(
            RemoteState.serializer(),
            """{"enabled":true,"running":true,"email":"a@b.fr",
                "domains":[{"id":"1","host":"jellyfin.example.com","kind":"app","app":"jellyfin"},
                           {"id":"2","host":"nas.local","kind":"ui"}],
                "apps":[{"stack":"jellyfin","name":"Jellyfin","address":"http://nas.local:8096","status":"running"}],
                "dnsChallenge":true,"publicIp":"203.0.113.9"}""",
        )
        assertEquals(listOf(PublishedLink("Jellyfin", "https://jellyfin.example.com")), publishedApps(s))
    }

    @Test
    fun fallsBackToTheLocalAddress() {
        val s = RemoteState(
            enabled = true,
            apps = listOf(PublishedApp(stack = "jellyfin", name = "Jellyfin", address = "http://192.168.1.10:8096", status = "running")),
        )
        assertEquals(listOf(PublishedLink("Jellyfin", "http://192.168.1.10:8096")), publishedApps(s))
    }

    @Test
    fun skipsAppsWithoutAddress() {
        val s = RemoteState(apps = listOf(PublishedApp(stack = "down", name = "Down", status = "exited")))
        assertEquals(emptyList<PublishedLink>(), publishedApps(s))
    }

    @Test
    fun parsesIconAndKeepsItInTheLink() {
        val s = json.decodeFromString(
            RemoteState.serializer(),
            """{"apps":[{"stack":"jellyfin","name":"Jellyfin","address":"http://nas:8096","status":"running",
                "icon":"https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/jellyfin.svg"}]}""",
        )
        assertEquals("https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/jellyfin.svg", publishedApps(s).single().icon)
    }

    @Test
    fun rasterIconVariants() {
        assertEquals(
            "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/jellyfin.png",
            rasterIconUrl("https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/jellyfin.svg"),
        )
        assertEquals(null, rasterIconUrl("https://example.com/icon.svg"))
        assertEquals("https://example.com/icon.png", rasterIconUrl("https://example.com/icon.png"))
        assertEquals(null, rasterIconUrl("not a url"))
    }
}
