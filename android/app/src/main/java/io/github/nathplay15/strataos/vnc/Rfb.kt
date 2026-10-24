package io.github.nathplay15.strataos.vnc

import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.EOFException
import java.io.IOException
import java.io.InputStream
import java.util.zip.Inflater

// A small RFB (VNC) client for the consoles of the virtual machines: the NAS
// relays the VM's VNC server (QEMU, no password, reachable on the NAS only)
// over a websocket. Encodings: ZRLE (compressed, for slow links), CopyRect,
// Raw, and the DesktopSize pseudo-encoding (the guest changes resolution).
// Pixels are 32-bit true colour, kept as ARGB ints ready for a Bitmap.

/** Bytes to and from the VNC server. */
interface RfbTransport {
    val input: InputStream
    fun write(b: ByteArray)
    fun close()
}

class RfbException(message: String) : IOException(message)

/** What the client tells its screen. */
interface RfbListener {
    fun onResize(width: Int, height: Int, name: String)
    /** Pixels changed in the rectangle (the framebuffer is up to date). */
    fun onUpdate(x: Int, y: Int, w: Int, h: Int)
    fun onBell() {}
    fun onClipboard(text: String) {}
}

const val ENC_RAW = 0
const val ENC_COPYRECT = 1
const val ENC_ZRLE = 16
const val ENC_DESKTOP_SIZE = -223

class RfbClient(private val t: RfbTransport, private val listener: RfbListener) {
    private val inp = DataInputStream(t.input)
    private val inflater = Inflater()
    var width = 0
        private set
    var height = 0
        private set
    var name = ""
        private set

    /** The screen, ARGB, [width] × [height]; read it under its own lock. */
    @Volatile var pixels = IntArray(0)
        private set

    @Volatile private var closed = false

    /** Handshake, then reads the server until the connection ends (blocking). */
    fun run() {
        try {
            handshake()
            setPixelFormat()
            setEncodings(intArrayOf(ENC_ZRLE, ENC_COPYRECT, ENC_RAW, ENC_DESKTOP_SIZE))
            requestUpdate(false)
            while (!closed) message()
        } finally {
            inflater.end()
        }
    }

    fun close() {
        closed = true
        t.close()
    }

    private fun handshake() {
        val ver = ByteArray(12)
        inp.readFully(ver)
        val v = String(ver, Charsets.US_ASCII)
        if (!v.startsWith("RFB ")) throw RfbException("not a VNC server")
        val minor = v.substring(8, 11).toIntOrNull() ?: 3
        val use = if (minor >= 8) "RFB 003.008\n" else if (minor >= 7) "RFB 003.007\n" else "RFB 003.003\n"
        t.write(use.toByteArray(Charsets.US_ASCII))
        val sec: Int
        if (minor >= 7) {
            val n = inp.readUnsignedByte()
            if (n == 0) throw RfbException(reason())
            val types = ByteArray(n).also { inp.readFully(it) }.map { it.toInt() and 0xff }
            if (1 !in types) throw RfbException("the console asks for a password, which the app does not support")
            t.write(byteArrayOf(1))
            sec = 1
        } else {
            sec = inp.readInt()
            if (sec == 0) throw RfbException(reason())
            if (sec != 1) throw RfbException("the console asks for a password, which the app does not support")
        }
        if (minor >= 8 || (sec != 1)) {
            if (inp.readInt() != 0) throw RfbException(if (minor >= 8) reason() else "VNC authentication failed")
        }
        t.write(byteArrayOf(1)) // shared: other viewers stay connected
        width = inp.readUnsignedShort()
        height = inp.readUnsignedShort()
        inp.readFully(ByteArray(16)) // the server's pixel format: replaced below
        name = readString()
        pixels = IntArray(width * height)
        listener.onResize(width, height, name)
    }

    private fun reason(): String = runCatching { readString() }.getOrDefault("connection refused by the VNC server")

    private fun readString(): String {
        val n = inp.readInt()
        if (n < 0 || n > 1 shl 20) throw RfbException("invalid string length")
        return String(ByteArray(n).also { inp.readFully(it) }, Charsets.ISO_8859_1)
    }

    /** 32 bits per pixel, depth 24, little endian, red at bit 16: the layout of an ARGB int. */
    private fun setPixelFormat() {
        t.write(byteArrayOf(0, 0, 0, 0, 32, 24, 0, 1, 0, 255.toByte(), 0, 255.toByte(), 0, 255.toByte(), 16, 8, 0, 0, 0, 0))
    }

    private fun setEncodings(enc: IntArray) {
        val b = Bytes()
        b.u8(2); b.u8(0); b.u16(enc.size)
        enc.forEach { b.s32(it) }
        t.write(b.done())
    }

    fun requestUpdate(incremental: Boolean, x: Int = 0, y: Int = 0, w: Int = width, h: Int = height) {
        val b = Bytes()
        b.u8(3); b.u8(if (incremental) 1 else 0); b.u16(x); b.u16(y); b.u16(w); b.u16(h)
        send(b.done())
    }

    /** A key (X11 keysym) pressed or released. */
    fun key(keysym: Int, down: Boolean) {
        val b = Bytes()
        b.u8(4); b.u8(if (down) 1 else 0); b.u16(0); b.s32(keysym)
        send(b.done())
    }

    /** The pointer at (x, y) with these buttons down (bit 0 left, 1 middle, 2 right, 3/4 wheel up/down). */
    fun pointer(x: Int, y: Int, buttons: Int) {
        val b = Bytes()
        b.u8(5); b.u8(buttons); b.u16(x.coerceIn(0, (width - 1).coerceAtLeast(0))); b.u16(y.coerceIn(0, (height - 1).coerceAtLeast(0)))
        send(b.done())
    }

    private fun send(b: ByteArray) {
        if (closed) return
        synchronized(this) { runCatching { t.write(b) } }
    }

    private fun message() {
        when (val type = inp.readUnsignedByte()) {
            0 -> update()
            1 -> { // colour map: unused in true colour
                inp.readUnsignedByte(); inp.readUnsignedShort()
                val n = inp.readUnsignedShort()
                inp.readFully(ByteArray(n * 6))
            }
            2 -> listener.onBell()
            3 -> {
                inp.readFully(ByteArray(3))
                listener.onClipboard(readString())
            }
            else -> throw RfbException("unknown VNC message $type")
        }
    }

    private fun update() {
        inp.readUnsignedByte()
        val n = inp.readUnsignedShort()
        repeat(n) {
            val x = inp.readUnsignedShort()
            val y = inp.readUnsignedShort()
            val w = inp.readUnsignedShort()
            val h = inp.readUnsignedShort()
            when (val enc = inp.readInt()) {
                ENC_RAW -> raw(x, y, w, h)
                ENC_COPYRECT -> copyRect(x, y, w, h)
                ENC_ZRLE -> zrle(x, y, w, h)
                ENC_DESKTOP_SIZE -> {
                    width = w; height = h
                    pixels = IntArray(w * h)
                    listener.onResize(w, h, name)
                }
                else -> throw RfbException("unsupported VNC encoding $enc")
            }
            if (w > 0 && h > 0) listener.onUpdate(x, y, w, h)
        }
        requestUpdate(true)
    }

    private fun checkRect(x: Int, y: Int, w: Int, h: Int) {
        if (x + w > width || y + h > height) throw RfbException("rectangle outside the screen")
    }

    private fun raw(x: Int, y: Int, w: Int, h: Int) {
        checkRect(x, y, w, h)
        val row = ByteArray(w * 4)
        val px = pixels
        for (j in 0 until h) {
            inp.readFully(row)
            var o = (y + j) * width + x
            for (i in 0 until w) {
                val k = i * 4
                px[o++] = argb(row[k + 2], row[k + 1], row[k])
            }
        }
    }

    private fun copyRect(x: Int, y: Int, w: Int, h: Int) {
        val sx = inp.readUnsignedShort()
        val sy = inp.readUnsignedShort()
        checkRect(x, y, w, h)
        checkRect(sx, sy, w, h)
        val px = pixels
        val tmp = IntArray(w * h)
        for (j in 0 until h) System.arraycopy(px, (sy + j) * width + sx, tmp, j * w, w)
        for (j in 0 until h) System.arraycopy(tmp, j * w, px, (y + j) * width + x, w)
    }

    private fun zrle(x: Int, y: Int, w: Int, h: Int) {
        checkRect(x, y, w, h)
        val len = inp.readInt()
        if (len < 0 || len > 64 shl 20) throw RfbException("invalid ZRLE length")
        val data = ByteArray(len).also { inp.readFully(it) }
        inflater.setInput(data)
        decodeZrle(InflaterReader(inflater), pixels, width, x, y, w, h)
    }
}

fun argb(r: Byte, g: Byte, b: Byte): Int =
    (0xff shl 24) or ((r.toInt() and 0xff) shl 16) or ((g.toInt() and 0xff) shl 8) or (b.toInt() and 0xff)

/** Reads the decompressed bytes of a ZRLE rectangle. */
interface ByteSource {
    fun u8(): Int
    /** A compressed pixel: blue, green, red (the 3 low bytes, little endian). */
    fun cpixel(): Int = (0xff shl 24) or u8() or (u8() shl 8) or (u8() shl 16)
}

class InflaterReader(private val inf: Inflater) : ByteSource {
    private val buf = ByteArray(16 * 1024)
    private var pos = 0
    private var len = 0
    override fun u8(): Int {
        if (pos == len) {
            len = inf.inflate(buf)
            pos = 0
            if (len == 0) {
                if (inf.needsInput()) throw EOFException("truncated ZRLE data")
                throw RfbException("invalid ZRLE data")
            }
        }
        return buf[pos++].toInt() and 0xff
    }
}

class ArraySource(private val b: ByteArray) : ByteSource {
    private var i = 0
    override fun u8(): Int {
        if (i >= b.size) throw EOFException("truncated ZRLE data")
        return b[i++].toInt() and 0xff
    }
}

/** Decodes the 64×64 tiles of a ZRLE rectangle into [px] (screen [stride] pixels wide). */
fun decodeZrle(s: ByteSource, px: IntArray, stride: Int, x: Int, y: Int, w: Int, h: Int) {
    val palette = IntArray(128)
    var ty = 0
    while (ty < h) {
        val th = minOf(64, h - ty)
        var tx = 0
        while (tx < w) {
            val tw = minOf(64, w - tx)
            val ox = x + tx
            val oy = y + ty
            val sub = s.u8()
            when {
                sub == 0 -> for (j in 0 until th) for (i in 0 until tw) px[(oy + j) * stride + ox + i] = s.cpixel()
                sub == 1 -> {
                    val c = s.cpixel()
                    for (j in 0 until th) java.util.Arrays.fill(px, (oy + j) * stride + ox, (oy + j) * stride + ox + tw, c)
                }
                sub in 2..16 -> {
                    for (k in 0 until sub) palette[k] = s.cpixel()
                    val bits = when { sub == 2 -> 1; sub <= 4 -> 2; else -> 4 }
                    val mask = (1 shl bits) - 1
                    for (j in 0 until th) {
                        var byte = 0
                        var left = 0
                        for (i in 0 until tw) {
                            if (left == 0) { byte = s.u8(); left = 8 }
                            left -= bits
                            px[(oy + j) * stride + ox + i] = palette[(byte shr left) and mask]
                        }
                    }
                }
                sub == 128 -> runs(s, px, stride, ox, oy, tw, th) { s.cpixel() to runLength(s) }
                sub >= 130 -> {
                    val n = sub - 128
                    for (k in 0 until n) palette[k] = s.cpixel()
                    runs(s, px, stride, ox, oy, tw, th) {
                        val idx = s.u8()
                        palette[idx and 127] to if (idx and 128 != 0) runLength(s) else 1
                    }
                }
                else -> throw RfbException("invalid ZRLE tile $sub")
            }
            tx += 64
        }
        ty += 64
    }
}

private fun runLength(s: ByteSource): Int {
    var n = 1
    while (true) {
        val b = s.u8()
        n += b
        if (b != 255) return n
    }
}

private inline fun runs(s: ByteSource, px: IntArray, stride: Int, ox: Int, oy: Int, tw: Int, th: Int, next: () -> Pair<Int, Int>) {
    val total = tw * th
    var done = 0
    while (done < total) {
        val (c, n) = next()
        if (n > total - done) throw RfbException("ZRLE run past the tile")
        repeat(n) {
            val j = done / tw
            val i = done % tw
            px[(oy + j) * stride + ox + i] = c
            done++
        }
    }
}

/** A message being built (network byte order). */
class Bytes {
    private val o = ByteArrayOutputStream(32)
    fun u8(v: Int) = o.write(v)
    fun u16(v: Int) { o.write(v shr 8); o.write(v) }
    fun s32(v: Int) { o.write(v ushr 24); o.write(v shr 16); o.write(v shr 8); o.write(v) }
    fun done(): ByteArray = o.toByteArray()
}
