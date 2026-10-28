package io.github.nathplay15.strataos.term

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Typeface
import android.text.InputType
import android.util.TypedValue
import android.view.GestureDetector
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputMethodManager

/**
 * Draws a [TermScreen] and turns the keyboard into terminal input: the soft
 * keyboard (without suggestions, so that every key is sent at once) and a
 * hardware one. Scrolling with a finger shows the lines above the screen.
 */
@SuppressLint("ViewConstructor")
class TerminalView(context: Context, val screen: TermScreen, private val send: (String) -> Unit, private val onResize: (Int, Int) -> Unit) : View(context) {
    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        typeface = Typeface.MONOSPACE
        textSize = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_SP, 12f, resources.displayMetrics)
    }
    private val bgPaint = Paint()
    private var cw = paint.measureText("M")
    private var ch = paint.fontSpacing
    /** Lines of scrollback shown above the screen (0: the live screen). */
    private var scroll = 0
    private var scrollAcc = 0f
    /** Ctrl from the key row: applies to the next key. */
    var ctrl = false
    var onCtrlUsed: () -> Unit = {}

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        setBackgroundColor(BG)
    }

    private val gestures = GestureDetector(context, object : GestureDetector.SimpleOnGestureListener() {
        override fun onDown(e: MotionEvent) = true
        override fun onSingleTapUp(e: MotionEvent): Boolean {
            showKeyboard()
            return true
        }
        override fun onScroll(e1: MotionEvent?, e2: MotionEvent, dx: Float, dy: Float): Boolean {
            scrollAcc -= dy
            val lines = (scrollAcc / ch).toInt()
            if (lines != 0) {
                scrollAcc -= lines * ch
                scroll = (scroll + lines).coerceIn(0, screen.scrollback.size)
                invalidate()
            }
            return true
        }
    })

    @SuppressLint("ClickableViewAccessibility")
    override fun onTouchEvent(event: MotionEvent) = gestures.onTouchEvent(event) || super.onTouchEvent(event)

    fun showKeyboard() {
        requestFocus()
        context.getSystemService(InputMethodManager::class.java)?.showSoftInput(this, InputMethodManager.SHOW_IMPLICIT)
    }

    fun setFontSize(sp: Float) {
        paint.textSize = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_SP, sp, resources.displayMetrics)
        cw = paint.measureText("M")
        ch = paint.fontSpacing
        fit(width, height)
    }

    override fun onSizeChanged(w: Int, h: Int, ow: Int, oh: Int) = fit(w, h)

    private fun fit(w: Int, h: Int) {
        if (w <= 0 || h <= 0) return
        val c = maxOf(10, (w / cw).toInt())
        val r = maxOf(4, (h / ch).toInt())
        screen.resize(c, r)
        onResize(c, r)
        invalidate()
    }

    /** New output: back to the live screen. */
    fun changed() {
        scroll = 0
        invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        val ascent = -paint.ascent()
        val sb = screen.scrollback
        for (row in 0 until screen.rows) {
            val idx = row - scroll
            val line = if (idx >= 0) screen.line(idx) else sb.getOrNull(sb.size + idx) ?: continue
            val top = row * ch
            var col = 0
            while (col < line.size) {
                val c0 = line[col]
                // A run of cells with the same look, drawn at once.
                var end = col + 1
                while (end < line.size && line[end].fg == c0.fg && line[end].bg == c0.bg && line[end].a == c0.a) end++
                var fg = color(c0.fg, true, c0.a and TermScreen.BOLD != 0)
                var bg = color(c0.bg, false, false)
                if (c0.a and TermScreen.REVERSE != 0) { val t = fg; fg = bg; bg = t }
                if (bg != BG) {
                    bgPaint.color = bg
                    canvas.drawRect(col * cw, top, end * cw, top + ch, bgPaint)
                }
                val text = String(CharArray(end - col) { line[col + it].c })
                if (text.isNotBlank()) {
                    paint.color = if (c0.a and TermScreen.DIM != 0) (fg and 0x00ffffff) or 0xa0000000.toInt() else fg
                    paint.isFakeBoldText = c0.a and TermScreen.BOLD != 0
                    paint.isUnderlineText = c0.a and TermScreen.UNDERLINE != 0
                    paint.textSkewX = if (c0.a and TermScreen.ITALIC != 0) -0.2f else 0f
                    // Cell by cell when the font's advance differs from the grid (wide characters).
                    if (kotlin.math.abs(paint.measureText(text) - text.length * cw) < 1f) canvas.drawText(text, col * cw, top + ascent, paint)
                    else text.forEachIndexed { i, c -> if (c != ' ') canvas.drawText(c.toString(), (col + i) * cw, top + ascent, paint) }
                }
                col = end
            }
        }
        if (screen.cursorVisible && scroll == 0) {
            bgPaint.color = CURSOR
            val cx = minOf(screen.x, screen.cols - 1)
            canvas.drawRect(cx * cw, screen.y * ch, (cx + 1) * cw, (screen.y + 1) * ch, bgPaint)
            paint.color = BG
            paint.isFakeBoldText = false
            paint.isUnderlineText = false
            paint.textSkewX = 0f
            canvas.drawText(screen.line(screen.y)[cx].c.toString(), cx * cw, screen.y * ch + ascent, paint)
        }
    }

    // ---- Input ----

    override fun onCheckIsTextEditor() = true

    override fun onCreateInputConnection(out: EditorInfo): InputConnection {
        out.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        out.imeOptions = EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_ACTION_NONE
        return object : BaseInputConnection(this, false) {
            override fun commitText(text: CharSequence, pos: Int): Boolean {
                typed(text.toString())
                return true
            }
            override fun deleteSurroundingText(before: Int, after: Int): Boolean {
                repeat(maxOf(before, 1)) { send("\u007f") }
                return true
            }
            override fun sendKeyEvent(event: KeyEvent): Boolean {
                if (event.action == KeyEvent.ACTION_DOWN) onKeyDown(event.keyCode, event)
                return true
            }
        }
    }

    /** Text from the keyboard, with Ctrl of the key row applied to its first character. */
    fun typed(s: String) {
        if (s.isEmpty()) return
        val t = s.replace('\n', '\r')
        if (ctrl) {
            ctrl = false
            onCtrlUsed()
            send(TermKeys.ctrl(t[0]) + t.substring(1))
        } else send(t)
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        val app = screen.appCursor
        val named = when (keyCode) {
            KeyEvent.KEYCODE_DEL -> "backspace"
            KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> "enter"
            KeyEvent.KEYCODE_TAB -> "tab"
            KeyEvent.KEYCODE_ESCAPE -> "esc"
            KeyEvent.KEYCODE_DPAD_UP -> "up"
            KeyEvent.KEYCODE_DPAD_DOWN -> "down"
            KeyEvent.KEYCODE_DPAD_LEFT -> "left"
            KeyEvent.KEYCODE_DPAD_RIGHT -> "right"
            KeyEvent.KEYCODE_MOVE_HOME -> "home"
            KeyEvent.KEYCODE_MOVE_END -> "end"
            KeyEvent.KEYCODE_PAGE_UP -> "pgup"
            KeyEvent.KEYCODE_PAGE_DOWN -> "pgdn"
            KeyEvent.KEYCODE_FORWARD_DEL -> "delete"
            else -> null
        }
        if (named != null) {
            send(TermKeys.named(named, app))
            return true
        }
        if (keyCode == KeyEvent.KEYCODE_BACK) return super.onKeyDown(keyCode, event)
        val u = event.getUnicodeChar(event.metaState and (KeyEvent.META_CTRL_MASK or KeyEvent.META_ALT_MASK).inv())
        if (u == 0) return super.onKeyDown(keyCode, event)
        val c = u.toChar()
        val text = if (event.isCtrlPressed) TermKeys.ctrl(c) else c.toString()
        if (event.isAltPressed) send("\u001b" + text) else if (event.isCtrlPressed) send(text) else typed(text)
        return true
    }

    companion object {
        val BG = Color.rgb(0x16, 0x18, 0x1d)
        val FG = Color.rgb(0xd8, 0xdc, 0xe2)
        val CURSOR = Color.rgb(0x9f, 0xb4, 0xd8)
        private val BASE = intArrayOf(
            0x1d1f24, 0xe06c75, 0x98c379, 0xe5c07b, 0x61afef, 0xc678dd, 0x56b6c2, 0xd0d4da,
            0x5c6370, 0xff7b86, 0xb5e890, 0xffd68a, 0x82c4ff, 0xde9cf5, 0x7fd9e4, 0xffffff,
        )

        /** The colour of a cell value (xterm 16, 256 and true colours). */
        fun color(v: Int, fg: Boolean, bold: Boolean): Int = when {
            v == TermScreen.DEFAULT -> if (fg) FG else BG
            v and TermScreen.RGB != 0 -> 0xff000000.toInt() or (v and 0xffffff)
            v < 8 && fg && bold -> 0xff000000.toInt() or BASE[v + 8]
            v < 16 -> 0xff000000.toInt() or BASE[v]
            v < 232 -> {
                val i = v - 16
                val steps = intArrayOf(0, 95, 135, 175, 215, 255)
                Color.rgb(steps[i / 36], steps[i / 6 % 6], steps[i % 6])
            }
            else -> (8 + (v - 232) * 10).let { Color.rgb(it, it, it) }
        }
    }
}
