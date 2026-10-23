package io.github.nathplay15.strataos.data

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Encrypts small secrets; the key never leaves where it lives (Android Keystore). */
interface SecretCipher {
    fun encrypt(plain: ByteArray): ByteArray
    fun decrypt(sealed: ByteArray): ByteArray
}

/** A string key/value store (SharedPreferences, or a map in tests). */
interface KeyValue {
    fun get(key: String): String?
    fun put(key: String, value: String?)
}

/** AES-GCM with a key generated in, and bound to, the Android Keystore. */
open class GcmCipher(private val key: () -> SecretKey) : SecretCipher {
    override fun encrypt(plain: ByteArray): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, key())
        return byteArrayOf(c.iv.size.toByte()) + c.iv + c.doFinal(plain)
    }

    override fun decrypt(sealed: ByteArray): ByteArray {
        val n = sealed[0].toInt()
        require(n in 12..16 && sealed.size > n + 1) { "invalid secret" }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, sealed, 1, n))
        return c.doFinal(sealed, 1 + n, sealed.size - 1 - n)
    }
}

/**
 * The token survives a reboot and works for background backups without user
 * presence, so the key is not bound to biometric authentication: the app lock
 * protects the interface, the Keystore keeps the token off backups and other
 * apps (android:allowBackup is off as well).
 */
class KeystoreCipher(alias: String = "strataos-device-token") : GcmCipher({ keystoreKey(alias) })

private fun keystoreKey(alias: String): SecretKey {
    val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    (ks.getKey(alias, null) as? SecretKey)?.let { return it }
    val g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
    g.init(
        KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .build(),
    )
    return g.generateKey()
}

/** Keeps the device token encrypted in [store]; a value that cannot be decrypted (key lost after a restore) reads as signed out. */
class TokenVault(private val store: KeyValue, private val cipher: SecretCipher, private val b64: Codec = AndroidBase64) {
    fun save(token: String?) {
        store.put(KEY, token?.let { b64.encode(cipher.encrypt(it.toByteArray())) })
    }

    fun load(): String? {
        val v = store.get(KEY) ?: return null
        return runCatching { String(cipher.decrypt(b64.decode(v))) }.getOrNull()
    }

    interface Codec {
        fun encode(b: ByteArray): String
        fun decode(s: String): ByteArray
    }

    object AndroidBase64 : Codec {
        override fun encode(b: ByteArray): String = Base64.encodeToString(b, Base64.NO_WRAP)
        override fun decode(s: String): ByteArray = Base64.decode(s, Base64.NO_WRAP)
    }

    private companion object {
        const val KEY = "token"
    }
}
