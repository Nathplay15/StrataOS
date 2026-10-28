package io.github.nathplay15.strataos.vnc

import java.io.IOException
import java.io.InputStream
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

/**
 * The RFB stream over the websocket of the NAS (GET /api/vms/{name}/vnc),
 * with the app's pinned client and device token. Binary messages are
 * concatenated into one input stream.
 */
class WsTransport(http: OkHttpClient, request: Request) : RfbTransport {
    private val queue = LinkedBlockingQueue<ByteArray>()
    @Volatile private var failure: IOException? = null
    private val end = ByteArray(0)
    private val ws: WebSocket

    init {
        val client = http.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).pingInterval(20, TimeUnit.SECONDS).build()
        ws = client.newWebSocket(request.newBuilder().header("Sec-WebSocket-Protocol", "binary").build(), object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, bytes: ByteString) { queue.put(bytes.toByteArray()) }
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                if (reason.isNotEmpty()) failure = IOException(reason)
                webSocket.close(1000, null)
                queue.put(end)
            }
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) { queue.put(end) }
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                failure = if (response != null && !response.isSuccessful) {
                    io.github.nathplay15.strataos.net.Api.apiError(response.code, runCatching { response.body.string() }.getOrDefault(""))
                } else t as? IOException ?: IOException(t)
                queue.put(end)
            }
        })
    }

    override val input: InputStream = object : InputStream() {
        private var cur = ByteArray(0)
        private var pos = 0
        private var eof = false

        private fun fill(): Boolean {
            while (pos >= cur.size) {
                if (eof) return false
                val next = queue.take()
                if (next === end) {
                    eof = true
                    failure?.let { throw it }
                    return false
                }
                cur = next
                pos = 0
            }
            return true
        }

        override fun read(): Int = if (fill()) cur[pos++].toInt() and 0xff else -1

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            if (len == 0) return 0
            if (!fill()) return -1
            val n = minOf(len, cur.size - pos)
            System.arraycopy(cur, pos, b, off, n)
            pos += n
            return n
        }
    }

    override fun write(b: ByteArray) {
        if (!ws.send(b.toByteString())) throw IOException("console disconnected")
    }

    override fun close() {
        ws.close(1000, null)
        queue.put(end)
    }
}
