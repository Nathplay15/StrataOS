package io.github.nathplay15.strataos

import io.github.nathplay15.strataos.data.GcmCipher
import io.github.nathplay15.strataos.data.KeyValue
import io.github.nathplay15.strataos.data.TokenVault
import java.util.Base64
import javax.crypto.KeyGenerator
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Test

class TokenVaultTest {
    private class MapKv : KeyValue {
        val m = HashMap<String, String>()
        override fun get(key: String) = m[key]
        override fun put(key: String, value: String?) { if (value == null) m.remove(key) else m[key] = value }
    }

    private object B64 : TokenVault.Codec {
        override fun encode(b: ByteArray): String = Base64.getEncoder().encodeToString(b)
        override fun decode(s: String): ByteArray = Base64.getDecoder().decode(s)
    }

    // Plain JCA key for the JVM; the app uses one kept in the Android Keystore.
    private fun cipher() = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().let { k -> GcmCipher { k } }

    @Test fun storesTheTokenEncrypted() {
        val kv = MapKv()
        val v = TokenVault(kv, cipher(), B64)
        val tok = "stdev_" + "ab".repeat(32)
        v.save(tok)
        assertEquals(tok, v.load())
        assertFalse(kv.m.values.single().contains("stdev_"))
        val first = kv.m.values.single()
        v.save(tok)
        assertNotEquals(first, kv.m.values.single()) // fresh IV each time
        v.save(null)
        assertNull(v.load())
    }

    @Test fun unreadableTokenMeansSignedOut() {
        val kv = MapKv()
        TokenVault(kv, cipher(), B64).save("stdev_x")
        assertNull(TokenVault(kv, cipher(), B64).load()) // key lost (backup restored elsewhere)
        kv.m["token"] = "garbage"
        assertNull(TokenVault(kv, cipher(), B64).load())
    }
}
