package io.github.nathplay15.strataos

import android.content.Context
import android.os.Build
import io.github.nathplay15.strataos.backup.BackupDb
import io.github.nathplay15.strataos.data.AppPrefs
import io.github.nathplay15.strataos.data.ServerConfig
import io.github.nathplay15.strataos.net.Api
import io.github.nathplay15.strataos.net.nasHttpClient
import okhttp3.OkHttpClient

/** The few shared objects of the app (no dependency injection framework). */
object Graph {
    lateinit var app: Context
        private set
    val prefs by lazy { AppPrefs(app) }
    val backupDb by lazy { BackupDb(app) }

    private var cached: Triple<ServerConfig, String?, Api>? = null

    fun init(context: Context) {
        app = context.applicationContext
    }

    val deviceModel: String get() = listOf(Build.MANUFACTURER.replaceFirstChar { it.uppercase() }, Build.MODEL).distinct().joinToString(" ")

    /** The API of the paired NAS, or null when signed out. */
    @Synchronized
    fun api(): Api? {
        val cfg = prefs.server ?: return null
        val tok = prefs.vault.load() ?: return null
        cached?.let { (c, t, a) -> if (c.url == cfg.url && c.fingerprint == cfg.fingerprint && c.caOnly == cfg.caOnly && t == tok) return a }
        val a = Api(cfg.url, client(cfg), tok)
        cached = Triple(cfg, tok, a)
        return a
    }

    fun client(cfg: ServerConfig): OkHttpClient = nasHttpClient(cfg.fingerprint.ifEmpty { null }, cfg.caOnly)

    @Synchronized
    fun reset() {
        cached = null
    }
}
