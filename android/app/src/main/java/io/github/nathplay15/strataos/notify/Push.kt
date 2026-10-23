package io.github.nathplay15.strataos.notify

import android.app.Activity
import android.content.Context
import io.github.nathplay15.strataos.BuildConfig
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.net.json
import kotlin.concurrent.thread
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.UnifiedPush
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage

/**
 * Notifications without Google services: UnifiedPush. A distributor app on
 * the phone (ntfy, NextPush…) gives an endpoint URL; the NAS posts Web Push
 * messages (RFC 8291, encrypted for this app, VAPID-signed) to it, the
 * distributor wakes this service, the connector decrypts.
 */
class StrataPushService : PushService() {
    override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) {
        val keys = endpoint.pubKeySet
        thread {
            runCatching {
                Graph.api()?.registerPush(endpoint.url, keys?.pubKey ?: "", keys?.auth ?: "", BuildConfig.VERSION_NAME)
                Graph.prefs.pushEndpoint = endpoint.url
            }
        }
    }

    override fun onMessage(message: PushMessage, instance: String) {
        val o = runCatching { json.decodeFromString(JsonObject.serializer(), String(message.content)) }.getOrNull() ?: return
        if (o["type"]?.jsonPrimitive?.contentOrNull != "notification") return
        EventSync.show(
            applicationContext,
            o["id"]?.jsonPrimitive?.longOrNull ?: return,
            o["level"]?.jsonPrimitive?.contentOrNull ?: "info",
            o["subject"]?.jsonPrimitive?.contentOrNull ?: "",
            o["message"]?.jsonPrimitive?.contentOrNull ?: "",
            o["host"]?.jsonPrimitive?.contentOrNull ?: "",
        )
    }

    override fun onRegistrationFailed(reason: FailedReason, instance: String) {
        Graph.prefs.pushEndpoint = ""
    }

    override fun onUnregistered(instance: String) {
        Graph.prefs.pushEndpoint = ""
        thread { runCatching { Graph.api()?.registerPush("", "", "", BuildConfig.VERSION_NAME) } }
    }
}

object Push {
    /** Distributors installed on the phone (empty: polling only). */
    fun distributors(ctx: Context): List<String> = UnifiedPush.getDistributors(ctx)

    /** Registers with the saved or default distributor, with the NAS's VAPID key. */
    fun register(activity: Activity, vapid: String, onResult: (Boolean) -> Unit = {}) {
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { ok ->
            if (ok) runCatching { UnifiedPush.register(activity, "default", null, vapid.ifEmpty { null }) }
            onResult(ok)
        }
    }

    fun unregister(ctx: Context) {
        runCatching { UnifiedPush.unregister(ctx, "default") }
    }
}
