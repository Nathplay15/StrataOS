package io.github.nathplay15.strataos.net

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Handler
import android.os.Looper

/** A NAS found on the local network (StrataOS announces _strataos._tcp with Avahi). */
data class FoundNas(val name: String, val host: String, val port: Int) {
    val url get() = "https://" + (if (host.contains(':')) "[$host]" else host) + ":" + port
}

/** mDNS discovery with the platform NsdManager (no extra library). */
class Discovery(ctx: Context, private val onChange: (List<FoundNas>) -> Unit) {
    private val nsd = ctx.getSystemService(NsdManager::class.java)
    private val main = Handler(Looper.getMainLooper())
    private val found = linkedMapOf<String, FoundNas>()
    private var listener: NsdManager.DiscoveryListener? = null

    fun start() {
        if (listener != null) return
        val l = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(type: String) = Unit
            override fun onDiscoveryStopped(type: String) = Unit
            override fun onStartDiscoveryFailed(type: String, code: Int) = Unit
            override fun onStopDiscoveryFailed(type: String, code: Int) = Unit
            override fun onServiceFound(info: NsdServiceInfo) = resolve(info)
            override fun onServiceLost(info: NsdServiceInfo) {
                main.post { found.remove(info.serviceName); onChange(found.values.toList()) }
            }
        }
        listener = l
        runCatching { nsd.discoverServices("_strataos._tcp", NsdManager.PROTOCOL_DNS_SD, l) }
    }

    @Suppress("DEPRECATION") // resolveService is the only API before Android 14
    private fun resolve(info: NsdServiceInfo) {
        runCatching {
            nsd.resolveService(info, object : NsdManager.ResolveListener {
                override fun onResolveFailed(i: NsdServiceInfo, code: Int) = Unit
                override fun onServiceResolved(i: NsdServiceInfo) {
                    val host = i.host?.hostAddress?.substringBefore('%') ?: return
                    val port = i.attributes["https"]?.let { String(it).toIntOrNull() } ?: i.port
                    main.post {
                        found[i.serviceName] = FoundNas(i.serviceName.removePrefix("StrataOS on "), host, port)
                        onChange(found.values.toList())
                    }
                }
            })
        }
    }

    fun stop() {
        listener?.let { runCatching { nsd.stopServiceDiscovery(it) } }
        listener = null
    }
}
