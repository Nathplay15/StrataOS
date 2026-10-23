package io.github.nathplay15.strataos.data

import android.content.Context
import android.content.SharedPreferences
import androidx.core.content.edit
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/** The paired NAS (one per app install in this version). */
@Serializable
data class ServerConfig(
    val url: String, // https://host:7443 (no trailing slash)
    val fingerprint: String = "", // pinned certificate, "" for CA-only
    val caOnly: Boolean = false,
    val username: String = "",
    val fullName: String = "",
    val role: String = "user",
    val hostname: String = "",
    val deviceId: String = "",
    val deviceName: String = "",
) {
    val isAdmin get() = role == "admin"
}

class PrefsKeyValue(private val p: SharedPreferences) : KeyValue {
    override fun get(key: String): String? = p.getString(key, null)
    override fun put(key: String, value: String?) = p.edit { if (value == null) remove(key) else putString(key, value) }
}

/** Server configuration, token and app preferences. */
class AppPrefs(context: Context) {
    private val p = context.getSharedPreferences("strataos", Context.MODE_PRIVATE)
    private val json = Json { ignoreUnknownKeys = true }
    val vault = TokenVault(PrefsKeyValue(context.getSharedPreferences("strataos-secret", Context.MODE_PRIVATE)), KeystoreCipher())

    var server: ServerConfig?
        get() = p.getString("server", null)?.let { runCatching { json.decodeFromString<ServerConfig>(it) }.getOrNull() }
        set(v) = p.edit { if (v == null) remove("server") else putString("server", json.encodeToString(ServerConfig.serializer(), v)) }

    var biometricLock: Boolean
        get() = p.getBoolean("biometricLock", false)
        set(v) = p.edit { putBoolean("biometricLock", v) }

    /** Highest NAS event already shown as a notification (push and polling share it). */
    var lastEventId: Long
        get() = p.getLong("lastEventId", -1)
        set(v) = p.edit { putLong("lastEventId", v) }

    var pushEndpoint: String
        get() = p.getString("pushEndpoint", "") ?: ""
        set(v) = p.edit { putString("pushEndpoint", v) }

    fun signOut() {
        vault.save(null)
        server = null
        p.edit { remove("lastEventId"); remove("pushEndpoint") }
    }
}
