package io.github.nathplay15.strataos.manage

import io.github.nathplay15.strataos.vnc.ArraySource
import io.github.nathplay15.strataos.vnc.Bytes
import io.github.nathplay15.strataos.vnc.Keysym
import io.github.nathplay15.strataos.vnc.RfbClient
import io.github.nathplay15.strataos.vnc.RfbException
import io.github.nathplay15.strataos.vnc.RfbListener
import io.github.nathplay15.strataos.vnc.RfbTransport
import io.github.nathplay15.strataos.vnc.SENTINEL
import io.github.nathplay15.strataos.vnc.charKeysym
import io.github.nathplay15.strataos.vnc.decodeZrle
import io.github.nathplay15.strataos.vnc.fitScale
import io.github.nathplay15.strataos.vnc.imeDiff
import io.github.nathplay15.strataos.vnc.viewToScreen
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.EOFException
import java.util.zip.Deflater
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

private const val RED = 0xffff0000.toInt()
private const val GREEN = 0xff00ff00.toInt()
private const val BLUE = 0xff0000ff.toInt()

/** A compressed pixel as the server sends it (blue, green, red). */
private fun cp(argb: Int) = byteArrayOf((argb and 0xff).toByte(), (argb shr 8 and 0xff).toByte(), (argb shr 16 and 0xff).toByte())

private fun bytes(vararg parts: Any): ByteArray {
    val o = ByteArrayOutputStream()
    parts.forEach { p ->
        when (p) {
            is ByteArray -> o.write(p)
            is Int -> o.write(p)
            else -> error("bad part")
        }
    }
    return o.toByteArray()
}

class ZrleTest {
    @Test fun solidRawAndPalette() {
        // A 4×2 rectangle: one tile each time.
        val px = IntArray(8)
        decodeZrle(ArraySource(bytes(1, cp(RED))), px, 4, 0, 0, 4, 2)
        assertTrue(px.all { it == RED })
        decodeZrle(ArraySource(bytes(0, *(0 until 8).map { cp(if (it % 2 == 0) GREEN else BLUE) }.toTypedArray())), px, 4, 0, 0, 4, 2)
        assertArrayEquals(IntArray(8) { if (it % 2 == 0) GREEN else BLUE }, px)
        // Palette of 2: 1 bit per pixel, rows padded to a byte: 1010 / 0101.
        decodeZrle(ArraySource(bytes(2, cp(RED), cp(BLUE), 0b10100000, 0b01010000)), px, 4, 0, 0, 4, 2)
        assertArrayEquals(intArrayOf(BLUE, RED, BLUE, RED, RED, BLUE, RED, BLUE), px)
    }

    @Test fun runs() {
        val px = IntArray(8)
        // Plain RLE: 3 red, 5 green (lengths are minus one).
        decodeZrle(ArraySource(bytes(128, cp(RED), 2, cp(GREEN), 4)), px, 4, 0, 0, 4, 2)
        assertArrayEquals(intArrayOf(RED, RED, RED, GREEN, GREEN, GREEN, GREEN, GREEN), px)
        // Palette RLE: index 0 once, index 1 (with run) for 7.
        decodeZrle(ArraySource(bytes(130, cp(BLUE), cp(RED), 0, 0x81, 6)), px, 4, 0, 0, 4, 2)
        assertArrayEquals(intArrayOf(BLUE, RED, RED, RED, RED, RED, RED, RED), px)
    }

    @Test fun subRectangleAndTiles() {
        // 70×1 inside a 80-pixel wide screen: two tiles (64 + 6), at x = 5.
        val px = IntArray(80)
        decodeZrle(ArraySource(bytes(1, cp(RED), 1, cp(BLUE))), px, 80, 5, 0, 70, 1)
        assertEquals(0, px[4]); assertEquals(RED, px[5]); assertEquals(RED, px[68]); assertEquals(BLUE, px[69]); assertEquals(BLUE, px[74]); assertEquals(0, px[75])
    }

    @Test fun malformed() {
        try { decodeZrle(ArraySource(bytes(128, cp(RED), 200)), IntArray(8), 4, 0, 0, 4, 2); fail("run past the tile") } catch (_: RfbException) {}
        try { decodeZrle(ArraySource(bytes(0, cp(RED))), IntArray(8), 4, 0, 0, 4, 2); fail("truncated") } catch (_: EOFException) {}
        try { decodeZrle(ArraySource(bytes(17)), IntArray(8), 4, 0, 0, 4, 2); fail("bad tile") } catch (_: RfbException) {}
    }
}

/** A VNC server played from bytes; what the client writes is kept. */
private class FakeServer(data: ByteArray) : RfbTransport {
    override val input = ByteArrayInputStream(data)
    val written = ByteArrayOutputStream()
    override fun write(b: ByteArray) { written.write(b) }
    override fun close() {}
}

class RfbClientTest {
    private fun serverInit(w: Int, h: Int) = Bytes().apply { u16(w); u16(h) }.done() + ByteArray(16) + Bytes().apply { s32(2) }.done() + "vm".toByteArray()

    @Test fun handshakeAndUpdates() {
        val z = Deflater()
        val raw = bytes(1, cp(GREEN)) // one solid tile
        z.setInput(raw); val out = ByteArray(64); val zn = z.deflate(out, 0, out.size, Deflater.SYNC_FLUSH)
        val data = bytes(
            "RFB 003.008\n".toByteArray(), 1, 1, Bytes().apply { s32(0) }.done(), serverInit(2, 2),
            // Update: a raw pixel at (1,0), then a ZRLE 2×1 at (0,1), then a CopyRect of (1,0) to (0,0).
            0, 0, Bytes().apply { u16(3) }.done(),
            Bytes().apply { u16(1); u16(0); u16(1); u16(1); s32(0) }.done(), byteArrayOf(0, 0, 0xff.toByte(), 0),
            Bytes().apply { u16(0); u16(1); u16(2); u16(1); s32(16); s32(zn) }.done(), out.copyOf(zn),
            Bytes().apply { u16(0); u16(0); u16(1); u16(1); s32(1); u16(1); u16(0) }.done(),
            // Resize to 3×1.
            0, 0, Bytes().apply { u16(1); u16(0); u16(0); u16(3); u16(1); s32(-223) }.done(),
        )
        val sizes = mutableListOf<Pair<Int, Int>>()
        var updates = 0
        val t = FakeServer(data)
        val c = RfbClient(t, object : RfbListener {
            override fun onResize(width: Int, height: Int, name: String) { sizes += width to height }
            override fun onUpdate(x: Int, y: Int, w: Int, h: Int) { updates++ }
        })
        var snapshot = IntArray(0)
        try { c.run() } catch (_: EOFException) {}
        assertEquals(listOf(2 to 2, 3 to 1), sizes)
        assertEquals(4, updates) // the resize counts as an update of the new screen
        assertEquals("vm", c.name)
        assertEquals(3, c.width)
        // The client answered 3.8, chose "None", asked to share, then set its pixel format.
        val w = t.written.toByteArray()
        assertEquals("RFB 003.008\n", String(w, 0, 12))
        assertEquals(1, w[12].toInt()); assertEquals(1, w[13].toInt())
        assertEquals(0, w[14].toInt()); assertEquals(32, w[18].toInt())
        snapshot = c.pixels
        assertEquals(3, snapshot.size)
    }

    @Test fun pixelsOfAnUpdate() {
        val data = bytes("RFB 003.008\n".toByteArray(), 1, 1, Bytes().apply { s32(0) }.done(), serverInit(2, 1),
            0, 0, Bytes().apply { u16(2) }.done(),
            Bytes().apply { u16(0); u16(0); u16(2); u16(1); s32(0) }.done(), byteArrayOf(0, 0, 0xff.toByte(), 0, 0xff.toByte(), 0, 0, 0),
            Bytes().apply { u16(1); u16(0); u16(1); u16(1); s32(1); u16(0); u16(0) }.done())
        val c = RfbClient(FakeServer(data), object : RfbListener {
            override fun onResize(width: Int, height: Int, name: String) {}
            override fun onUpdate(x: Int, y: Int, w: Int, h: Int) {}
        })
        try { c.run() } catch (_: EOFException) {}
        assertArrayEquals(intArrayOf(RED, RED), c.pixels) // raw red, blue; then red copied over blue
    }

    @Test fun passwordRefused() {
        val c = RfbClient(FakeServer(bytes("RFB 003.008\n".toByteArray(), 1, 2)), object : RfbListener {
            override fun onResize(width: Int, height: Int, name: String) {}
            override fun onUpdate(x: Int, y: Int, w: Int, h: Int) {}
        })
        try { c.run(); fail() } catch (e: RfbException) { assertTrue(e.message!!.contains("password")) }
    }

    @Test fun outsideRectangle() {
        val data = bytes("RFB 003.008\n".toByteArray(), 1, 1, Bytes().apply { s32(0) }.done(), serverInit(2, 2),
            0, 0, Bytes().apply { u16(1) }.done(), Bytes().apply { u16(1); u16(1); u16(5); u16(5); s32(0) }.done())
        val c = RfbClient(FakeServer(data), object : RfbListener {
            override fun onResize(width: Int, height: Int, name: String) {}
            override fun onUpdate(x: Int, y: Int, w: Int, h: Int) {}
        })
        try { c.run(); fail() } catch (_: RfbException) {}
    }
}

class KeysTest {
    @Test fun keysyms() {
        assertEquals('a'.code, charKeysym('a'.code))
        assertEquals(0xe9, charKeysym('é'.code))
        assertEquals(0x010020ac, charKeysym('€'.code))
        assertEquals(Keysym.RETURN, charKeysym('\n'.code))
    }

    @Test fun imeDiffs() {
        assertEquals(listOf('h'.code, 'i'.code), imeDiff(SENTINEL + "hi"))
        assertEquals(listOf(Keysym.BACKSPACE), imeDiff(SENTINEL.dropLast(1)))
        assertEquals(emptyList<Int>(), imeDiff(SENTINEL))
    }

    @Test fun coordinates() {
        assertEquals(0.5f, fitScale(1920, 1080, 960f, 1000f), 0.001f)
        assertEquals(100 to 50, viewToScreen(60f, 35f, 0.5f, 10f, 10f, 1920, 1080))
        assertEquals(0 to 1079, viewToScreen(-5f, 99999f, 0.5f, 0f, 0f, 1920, 1080))
    }
}
