package io.github.nathplay15.strataos.vnc

import android.view.KeyEvent

// X11 keysyms sent to the VM console, from typed text and from the keys of a
// hardware keyboard.

object Keysym {
    const val BACKSPACE = 0xff08
    const val TAB = 0xff09
    const val RETURN = 0xff0d
    const val ESCAPE = 0xff1b
    const val DELETE = 0xffff
    const val HOME = 0xff50
    const val LEFT = 0xff51
    const val UP = 0xff52
    const val RIGHT = 0xff53
    const val DOWN = 0xff54
    const val PAGE_UP = 0xff55
    const val PAGE_DOWN = 0xff56
    const val END = 0xff57
    const val INSERT = 0xff63
    const val F1 = 0xffbe
    const val SHIFT_L = 0xffe1
    const val CONTROL_L = 0xffe3
    const val ALT_L = 0xffe9
    const val SUPER_L = 0xffeb

    fun f(n: Int) = F1 + (n - 1)
}

/** The keysym of a typed character: Latin-1 as is, other characters as Unicode keysyms. */
fun charKeysym(c: Int): Int = when {
    c == '\n'.code || c == '\r'.code -> Keysym.RETURN
    c == '\t'.code -> Keysym.TAB
    c == 8 -> Keysym.BACKSPACE
    c in 0x20..0x7e || c in 0xa0..0xff -> c
    else -> 0x01000000 or c
}

/** Keysyms to send for text typed with the on-screen keyboard. */
fun textKeysyms(s: String): List<Int> = buildList { s.codePoints().forEach { add(charKeysym(it)) } }

/**
 * What the on-screen keyboard did to the text field: the field always holds
 * [SENTINEL] (so that backspace has something to delete); a shorter value is
 * backspaces, extra text is typed characters.
 */
const val SENTINEL = "  "

fun imeDiff(new: String): List<Int> = when {
    new.length < SENTINEL.length -> List(SENTINEL.length - new.length) { Keysym.BACKSPACE }
    new.startsWith(SENTINEL) -> textKeysyms(new.substring(SENTINEL.length))
    else -> textKeysyms(new.trimStart())
}

/** The keysym of a hardware key without a character (arrows, F keys…), or 0. */
fun keyCodeKeysym(code: Int): Int = when (code) {
    KeyEvent.KEYCODE_DEL -> Keysym.BACKSPACE
    KeyEvent.KEYCODE_FORWARD_DEL -> Keysym.DELETE
    KeyEvent.KEYCODE_TAB -> Keysym.TAB
    KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> Keysym.RETURN
    KeyEvent.KEYCODE_ESCAPE -> Keysym.ESCAPE
    KeyEvent.KEYCODE_DPAD_LEFT -> Keysym.LEFT
    KeyEvent.KEYCODE_DPAD_RIGHT -> Keysym.RIGHT
    KeyEvent.KEYCODE_DPAD_UP -> Keysym.UP
    KeyEvent.KEYCODE_DPAD_DOWN -> Keysym.DOWN
    KeyEvent.KEYCODE_MOVE_HOME -> Keysym.HOME
    KeyEvent.KEYCODE_MOVE_END -> Keysym.END
    KeyEvent.KEYCODE_PAGE_UP -> Keysym.PAGE_UP
    KeyEvent.KEYCODE_PAGE_DOWN -> Keysym.PAGE_DOWN
    KeyEvent.KEYCODE_INSERT -> Keysym.INSERT
    KeyEvent.KEYCODE_SHIFT_LEFT, KeyEvent.KEYCODE_SHIFT_RIGHT -> Keysym.SHIFT_L
    KeyEvent.KEYCODE_CTRL_LEFT, KeyEvent.KEYCODE_CTRL_RIGHT -> Keysym.CONTROL_L
    KeyEvent.KEYCODE_ALT_LEFT, KeyEvent.KEYCODE_ALT_RIGHT -> Keysym.ALT_L
    KeyEvent.KEYCODE_META_LEFT, KeyEvent.KEYCODE_META_RIGHT -> Keysym.SUPER_L
    in KeyEvent.KEYCODE_F1..KeyEvent.KEYCODE_F12 -> Keysym.f(code - KeyEvent.KEYCODE_F1 + 1)
    else -> 0
}

/**
 * Where a touch on the view falls on the VM screen: the screen is drawn
 * scaled by [scale] with its top-left corner at ([offX], [offY]).
 */
fun viewToScreen(x: Float, y: Float, scale: Float, offX: Float, offY: Float, w: Int, h: Int): Pair<Int, Int> =
    ((((x - offX) / scale).toInt()).coerceIn(0, (w - 1).coerceAtLeast(0))) to ((((y - offY) / scale).toInt()).coerceIn(0, (h - 1).coerceAtLeast(0)))

/** The scale that fits a [w]×[h] screen in a [vw]×[vh] view. */
fun fitScale(w: Int, h: Int, vw: Float, vh: Float): Float = if (w <= 0 || h <= 0) 1f else minOf(vw / w, vh / h)
