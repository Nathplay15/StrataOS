package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.update.AppVersion
import io.github.nathplay15.strataos.update.Asset
import io.github.nathplay15.strataos.update.Release
import io.github.nathplay15.strataos.update.Releases
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateTest {
    private fun v(s: String) = AppVersion.parse(s)!!

    @Test fun parsesVersionsTagsAndNames() {
        assertEquals(AppVersion(0, 10, 1), v("0.10.1"))
        assertEquals(AppVersion(0, 10, 1), v("v0.10.1"))
        assertEquals(AppVersion(0, 11, 0, "beta", 1), v("v0.11.0-beta.1"))
        assertEquals(AppVersion(0, 11, 0, "rc", 2), v("0.11.0~rc.2"))
        assertEquals("0.11.0~beta.1", v("v0.11.0-beta.1").toString())
        assertEquals("0.11.0-beta.1", v("0.11.0~beta.1").fileVersion)
        listOf("", "0.10", "v0.10.1.2", "x0.10.1", "0.10.01", "0.10.1-beta", "0.10.1-dev.1", "0.10.1-beta.0", "release-0.10.1").forEach {
            assertNull(it, AppVersion.parse(it))
        }
    }

    @Test fun ordersVersions() {
        val sorted = listOf("0.9.1", "0.10.0", "0.10.1", "0.11.0~alpha.1", "0.11.0~beta.1", "0.11.0~beta.2", "0.11.0~rc.1", "0.11.0", "1.0.0").map(::v)
        assertEquals(sorted, sorted.shuffled(java.util.Random(1)).sorted())
        assertTrue(Releases.isNewer(v("0.10.2"), v("0.10.1")))
        assertTrue(Releases.isNewer(v("0.11.0"), v("0.11.0~rc.1")))
        assertFalse(Releases.isNewer(v("0.10.1"), v("0.10.1")))
        assertFalse(Releases.isNewer(v("0.11.0~beta.1"), v("0.11.0")))
        assertFalse(Releases.isNewer(v("0.9.9"), v("0.10.0"))) // numeric, not text order
    }

    @Test fun versionCodes() {
        assertEquals(10_001L, v("0.10.1").versionCode)
        assertEquals(1_002_003L, v("1.2.3").versionCode)
        assertEquals(v("0.11.0").versionCode, v("0.11.0~beta.1").versionCode)
        assertEquals(v("0.10.1"), AppVersion.fromVersionCode(10_001))
        assertEquals(v("1.2.3"), AppVersion.fromVersionCode(1_002_003))
    }

    private fun rel(tag: String, prerelease: Boolean = false, draft: Boolean = false, vararg assets: String) =
        Release(tag = tag, prerelease = prerelease, draft = draft, assets = assets.map { Asset(it, "https://example/$it", 1) })

    @Test fun picksTheRelease() {
        val all = listOf(
            rel("v0.10.1"), rel("v0.11.0-beta.1", prerelease = true), rel("v0.12.0", draft = true),
            rel("nightly"), rel("v0.10.2-rc.1"), // a pre-release tag not flagged as such
        )
        assertEquals("v0.10.1", Releases.pick(all, beta = false)?.tag)
        assertEquals("v0.11.0-beta.1", Releases.pick(all, beta = true)?.tag)
        assertNull(Releases.pick(listOf(rel("v0.12.0", draft = true)), beta = true))
        assertNull(Releases.pick(emptyList(), beta = false))
    }

    @Test fun picksTheAssets() {
        val r = rel("v0.11.0-beta.1", true, false, "strataos_0.11.0-beta.1_amd64.deb", "strataos-0.11.0-beta.1.apk", "strataos-sync-0.11.0-beta.1-macos.dmg", "SHA256SUMS", "SHA256SUMS.asc")
        val f = Releases.files(r)!!
        assertEquals("strataos-0.11.0-beta.1.apk", f.apk.name)
        assertEquals("SHA256SUMS", f.sums.name)
        // An APK of another version or without checksums is not used.
        assertNull(Releases.files(rel("v0.10.2", false, false, "strataos-0.10.1.apk", "SHA256SUMS")))
        assertNull(Releases.files(rel("v0.10.2", false, false, "strataos-0.10.2.apk")))
    }

    @Test fun parsesGitHubJson() {
        val json = """{"url":"x","tag_name":"v0.10.2","name":"StrataOS 0.10.2","draft":false,"prerelease":false,
            "html_url":"https://github.com/Nathplay15/StrataOS/releases/tag/v0.10.2","body":"## Fixed\r\n- **A** fix",
            "assets":[{"name":"strataos-0.10.2.apk","size":123,"browser_download_url":"https://github.com/a.apk","uploader":{}},
            {"name":"SHA256SUMS","size":9,"browser_download_url":"https://github.com/SHA256SUMS"}]}"""
        val r = Json { ignoreUnknownKeys = true }.decodeFromString(Release.serializer(), json)
        assertEquals(v("0.10.2"), r.version)
        assertEquals(123L, Releases.files(r)!!.apk.size)
        assertEquals("Fixed\n- A fix", Releases.plainNotes(r.body))
    }

    @Test fun parsesSums() {
        val a = "a".repeat(64)
        val b = "B0".repeat(32)
        val text = "$a  strataos_0.10.2_amd64.deb\n$b *strataos-0.10.2.apk\r\nnot a line\n${"c".repeat(63)}  short.txt\n\n"
        val m = Releases.parseSums(text)
        assertEquals(mapOf("strataos_0.10.2_amd64.deb" to a, "strataos-0.10.2.apk" to b.lowercase()), m)
    }

    @Test fun comparesSigners() {
        assertTrue(Releases.sameSigner(setOf("k1"), setOf("k1")))
        assertFalse(Releases.sameSigner(setOf("k1"), setOf("k2")))
        assertFalse(Releases.sameSigner(emptySet(), emptySet()))
        // Key rotation: the new APK carries the installed key in its history.
        assertTrue(Releases.sameSigner(setOf("k1"), setOf("k2"), listOf("k1", "k2")))
        assertFalse(Releases.sameSigner(setOf("k1"), setOf("k2"), listOf("k3", "k2")))
        // Several signers: all of them must match.
        assertFalse(Releases.sameSigner(setOf("k1", "k2"), setOf("k1")))
    }

    @Test fun plainNotes() {
        assertEquals("Title\n\nSee docs and `x`".replace("`", ""), Releases.plainNotes("# Title\n\n\n\nSee [docs](https://x) and `x`"))
    }
}
