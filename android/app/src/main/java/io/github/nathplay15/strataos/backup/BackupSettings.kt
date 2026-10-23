package io.github.nathplay15.strataos.backup

import android.content.Context
import androidx.core.content.edit

/** What the photo backup sends and when. */
class BackupSettings(context: Context) {
    private val p = context.getSharedPreferences("backup", Context.MODE_PRIVATE)

    var enabled: Boolean
        get() = p.getBoolean("enabled", false)
        set(v) = p.edit { putBoolean("enabled", v) }
    var buckets: Set<String>
        get() = p.getStringSet("buckets", emptySet())!!.toSet()
        set(v) = p.edit { putStringSet("buckets", v) }
    var destination: String
        get() = p.getString("destination", "") ?: ""
        set(v) = p.edit { putString("destination", v) }
    var wifiOnly: Boolean
        get() = p.getBoolean("wifiOnly", true)
        set(v) = p.edit { putBoolean("wifiOnly", v) }
    var chargingOnly: Boolean
        get() = p.getBoolean("chargingOnly", false)
        set(v) = p.edit { putBoolean("chargingOnly", v) }
    var videos: Boolean
        get() = p.getBoolean("videos", true)
        set(v) = p.edit { putBoolean("videos", v) }
    var lastRun: Long
        get() = p.getLong("lastRun", 0)
        set(v) = p.edit { putLong("lastRun", v) }
    var lastResult: String
        get() = p.getString("lastResult", "") ?: ""
        set(v) = p.edit { putString("lastResult", v) }

    fun options() = BackupOptions(buckets, videos, destination)
}
