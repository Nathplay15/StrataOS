package io.github.nathplay15.strataos.term

/**
 * A small VT100/xterm screen for the container consoles: UTF-8 output of the
 * NAS in, a grid of cells out. It knows what shells, editors and pagers use
 * (cursor moves, erasing, insert/delete, scroll regions, colours and
 * attributes, the alternate screen, application cursor keys); other
 * sequences are read and ignored. [respond] carries the answers to cursor
 * position and device attribute queries back to the NAS.
 */
class TermScreen(cols: Int = 80, rows: Int = 24, private val respond: (String) -> Unit = {}) {
    class Cell(var c: Char = ' ', var fg: Int = DEFAULT, var bg: Int = DEFAULT, var a: Int = 0) {
        fun set(o: Cell) { c = o.c; fg = o.fg; bg = o.bg; a = o.a }
    }

    var cols = cols
        private set
    var rows = rows
        private set
    private var main = grid(cols, rows)
    private var alt = grid(cols, rows)
    private var lines = main
    val scrollback = ArrayDeque<Array<Cell>>()
    var altScreen = false
        private set

    var x = 0
        private set
    var y = 0
        private set
    private var wrapNext = false
    private var top = 0
    private var bottom = rows - 1
    private var fg = DEFAULT
    private var bg = DEFAULT
    private var attr = 0
    private var saved = IntArray(5)
    var cursorVisible = true
        private set
    /** DECCKM: arrows send ESC O A instead of ESC [ A. */
    var appCursor = false
        private set
    private var autowrap = true
    /** Bumped at every change (the view redraws). */
    var version = 0L
        private set

    // Parser
    private var state = 0
    private val params = ArrayList<Int>()
    private var cur = -1
    private var priv = ' '
    private var utfNeed = 0
    private var utfCode = 0

    private fun grid(c: Int, r: Int) = Array(r) { Array(c) { Cell() } }

    fun line(i: Int): Array<Cell> = lines[i]

    /** Feeds bytes from the NAS. */
    fun feed(b: ByteArray, off: Int = 0, len: Int = b.size) {
        for (i in off until off + len) {
            val v = b[i].toInt() and 0xff
            if (utfNeed > 0) {
                if (v and 0xc0 == 0x80) {
                    utfCode = (utfCode shl 6) or (v and 0x3f)
                    if (--utfNeed == 0) put(utfCode)
                    continue
                }
                utfNeed = 0
                put(0xfffd)
            }
            when {
                v < 0x80 -> put(v)
                v and 0xe0 == 0xc0 -> { utfNeed = 1; utfCode = v and 0x1f }
                v and 0xf0 == 0xe0 -> { utfNeed = 2; utfCode = v and 0x0f }
                v and 0xf8 == 0xf0 -> { utfNeed = 3; utfCode = v and 0x07 }
                else -> put(0xfffd)
            }
        }
        version++
    }

    fun feed(s: String) = feed(s.toByteArray())

    private fun put(cp: Int) {
        when (state) {
            S_ESC -> return esc(cp)
            S_CSI -> return csi(cp)
            S_OSC -> {
                if (cp == 7) state = S_GROUND
                else if (cp == 0x1b) state = S_OSC_ESC
                return
            }
            S_OSC_ESC -> { state = if (cp == '\\'.code) S_GROUND else S_OSC; return }
            S_CHARSET -> { state = S_GROUND; return }
        }
        when (cp) {
            0x1b -> state = S_ESC
            7, 0, 0x0e, 0x0f, 0x7f -> {}
            8 -> { if (x > 0) x--; wrapNext = false }
            9 -> { x = minOf(cols - 1, (x / 8 + 1) * 8); wrapNext = false }
            10, 11, 12 -> lineFeed()
            13 -> { x = 0; wrapNext = false }
            else -> if (cp >= 0x20) print(if (cp > 0xffff) '�' else cp.toChar())
        }
    }

    private fun print(ch: Char) {
        if (wrapNext) {
            wrapNext = false
            x = 0
            lineFeed()
        }
        val cell = lines[y][x]
        cell.c = ch
        cell.fg = fg
        cell.bg = bg
        cell.a = attr
        if (x == cols - 1) wrapNext = autowrap else x++
    }

    private fun lineFeed() {
        wrapNext = false
        if (y == bottom) scrollUp(1) else if (y < rows - 1) y++
    }

    private fun blank(): Cell = Cell(' ', fg, if (attr and REVERSE != 0) DEFAULT else bg, 0)

    private fun clearRow(r: Array<Cell>, from: Int = 0, to: Int = cols) {
        val b = blank()
        for (i in from until minOf(to, r.size)) r[i].set(b)
    }

    private fun scrollUp(n: Int) {
        repeat(minOf(n, bottom - top + 1)) {
            val gone = lines[top]
            if (!altScreen && top == 0) {
                scrollback.addLast(Array(cols) { i -> Cell().also { c -> c.set(gone[i]) } })
                while (scrollback.size > MAX_SCROLLBACK) scrollback.removeFirst()
            }
            for (r in top until bottom) lines[r] = lines[r + 1]
            lines[bottom] = Array(cols) { blank() }
        }
    }

    private fun scrollDown(n: Int) {
        repeat(minOf(n, bottom - top + 1)) {
            for (r in bottom downTo top + 1) lines[r] = lines[r - 1]
            lines[top] = Array(cols) { blank() }
        }
    }

    private fun esc(cp: Int) {
        state = S_GROUND
        when (cp.toChar()) {
            '[' -> { state = S_CSI; params.clear(); cur = -1; priv = ' ' }
            ']' -> state = S_OSC
            '(', ')', '*', '+' -> state = S_CHARSET
            '7' -> save()
            '8' -> restore()
            'D' -> lineFeed()
            'E' -> { x = 0; lineFeed() }
            'M' -> { wrapNext = false; if (y == top) scrollDown(1) else if (y > 0) y-- }
            'c' -> reset()
        }
    }

    private fun p(i: Int, def: Int = 1): Int = params.getOrNull(i)?.takeIf { it > 0 } ?: def

    private fun csi(cp: Int) {
        val ch = cp.toChar()
        when {
            ch in '0'..'9' -> { cur = (if (cur < 0) 0 else cur) * 10 + (ch - '0'); if (cur > 9999) cur = 9999; return }
            ch == ';' || ch == ':' -> { params += maxOf(cur, 0); cur = -1; return }
            ch == '?' || ch == '>' || ch == '=' || ch == '!' -> { priv = ch; return }
            cp in 0x20..0x2f -> return // intermediates
            cp < 0x20 -> { put0(cp); return }
        }
        if (cur >= 0) params += cur
        state = S_GROUND
        wrapNext = wrapNext && ch == 'm'
        when (ch) {
            '@' -> { val r = lines[y]; val n = minOf(p(0), cols - x); for (i in cols - 1 downTo x + n) r[i].set(r[i - n]); clearRow(r, x, x + n) }
            'A' -> y = maxOf(if (y >= top) top else 0, y - p(0))
            'B' -> y = minOf(if (y <= bottom) bottom else rows - 1, y + p(0))
            'C' -> x = minOf(cols - 1, x + p(0))
            'D' -> x = maxOf(0, minOf(x, cols - 1) - p(0))
            'E' -> { x = 0; y = minOf(rows - 1, y + p(0)) }
            'F' -> { x = 0; y = maxOf(0, y - p(0)) }
            'G', '`' -> x = (p(0) - 1).coerceIn(0, cols - 1)
            'H', 'f' -> { y = (p(0) - 1).coerceIn(0, rows - 1); x = (p(1) - 1).coerceIn(0, cols - 1) }
            'd' -> y = (p(0) - 1).coerceIn(0, rows - 1)
            'J' -> when (params.getOrNull(0) ?: 0) {
                0 -> { clearRow(lines[y], x); for (r in y + 1 until rows) clearRow(lines[r]) }
                1 -> { for (r in 0 until y) clearRow(lines[r]); clearRow(lines[y], 0, x + 1) }
                2, 3 -> { for (r in 0 until rows) clearRow(lines[r]); if (params.getOrNull(0) == 3) scrollback.clear() }
            }
            'K' -> when (params.getOrNull(0) ?: 0) {
                0 -> clearRow(lines[y], x)
                1 -> clearRow(lines[y], 0, x + 1)
                2 -> clearRow(lines[y])
            }
            'L' -> if (y in top..bottom) { val t = top; top = y; scrollDown(p(0)); top = t }
            'M' -> if (y in top..bottom) { val t = top; top = y; scrollUpNoSave(p(0)); top = t }
            'P' -> { val r = lines[y]; val n = minOf(p(0), cols - x); for (i in x until cols - n) r[i].set(r[i + n]); clearRow(r, cols - n) }
            'X' -> clearRow(lines[y], x, x + p(0))
            'S' -> scrollUpNoSave(p(0))
            'T' -> scrollDown(p(0))
            'm' -> sgr()
            'r' -> if (priv == ' ') {
                val t = p(0) - 1
                val b = p(1, rows) - 1
                if (t < b && b < rows) { top = t; bottom = b; x = 0; y = 0 }
            }
            's' -> save()
            'u' -> restore()
            'h', 'l' -> mode(ch == 'h')
            'n' -> if (p(0, 0) == 6) respond("\u001b[${y + 1};${minOf(x, cols - 1) + 1}R") else if (p(0, 0) == 5) respond("\u001b[0n")
            'c' -> if (priv == ' ') respond("\u001b[?1;2c")
        }
    }

    /** Control characters inside a CSI sequence are acted on at once. */
    private fun put0(cp: Int) {
        val s = state
        state = S_GROUND
        put(cp)
        if (cp != 0x1b) state = s
    }

    private fun scrollUpNoSave(n: Int) {
        val alt0 = altScreen
        altScreen = true // a deletion inside the screen does not feed the scrollback
        scrollUp(n)
        altScreen = alt0
    }

    private fun mode(on: Boolean) {
        if (priv != '?') return
        for (m in params) when (m) {
            1 -> appCursor = on
            7 -> autowrap = on
            25 -> cursorVisible = on
            47, 1047, 1049 -> if (on != altScreen) {
                if (m == 1049 && on) save()
                altScreen = on
                lines = if (on) alt else main
                if (on) for (r in lines) clearRow(r)
                if (m == 1049 && !on) restore()
            }
        }
    }

    private fun sgr() {
        if (params.isEmpty()) params += 0
        var i = 0
        while (i < params.size) {
            when (val v = params[i]) {
                0 -> { fg = DEFAULT; bg = DEFAULT; attr = 0 }
                1 -> attr = attr or BOLD
                2 -> attr = attr or DIM
                3 -> attr = attr or ITALIC
                4 -> attr = attr or UNDERLINE
                7 -> attr = attr or REVERSE
                22 -> attr = attr and (BOLD or DIM).inv()
                23 -> attr = attr and ITALIC.inv()
                24 -> attr = attr and UNDERLINE.inv()
                27 -> attr = attr and REVERSE.inv()
                in 30..37 -> fg = v - 30
                39 -> fg = DEFAULT
                in 40..47 -> bg = v - 40
                49 -> bg = DEFAULT
                in 90..97 -> fg = v - 90 + 8
                in 100..107 -> bg = v - 100 + 8
                38, 48 -> {
                    val c = when (params.getOrNull(i + 1)) {
                        5 -> params.getOrNull(i + 2)?.also { i += 2 }?.coerceIn(0, 255)
                        2 -> if (i + 4 < params.size) (RGB or (params[i + 2].coerceIn(0, 255) shl 16) or (params[i + 3].coerceIn(0, 255) shl 8) or params[i + 4].coerceIn(0, 255)).also { i += 4 } else null
                        else -> null
                    }
                    if (c != null) { if (v == 38) fg = c else bg = c }
                }
            }
            i++
        }
    }

    private fun save() { saved = intArrayOf(x, y, fg, bg, attr) }

    private fun restore() {
        x = saved[0].coerceIn(0, cols - 1); y = saved[1].coerceIn(0, rows - 1); fg = saved[2]; bg = saved[3]; attr = saved[4]
        wrapNext = false
    }

    private fun reset() {
        fg = DEFAULT; bg = DEFAULT; attr = 0
        top = 0; bottom = rows - 1; x = 0; y = 0
        appCursor = false; cursorVisible = true; autowrap = true
        altScreen = false
        lines = main
        for (r in lines) clearRow(r)
    }

    /** Changes the size, keeping the text at the top left (lines pushed out above the cursor go to the scrollback). */
    fun resize(c: Int, r: Int) {
        if (c == cols && r == rows || c < 2 || r < 2) return
        val shift = if (y >= r) y - r + 1 else 0
        fun copy(row: Array<Cell>?) = Array(c) { col -> Cell().also { cell -> row?.getOrNull(col)?.let { cell.set(it) } } }
        if (!altScreen) for (i in 0 until shift) scrollback.addLast(copy(main[i]))
        main = Array(r) { copy(main.getOrNull(it + if (altScreen) 0 else shift)) }
        alt = Array(r) { copy(alt.getOrNull(it + if (altScreen) shift else 0)) }
        lines = if (altScreen) alt else main
        cols = c
        rows = r
        y -= shift
        x = x.coerceIn(0, c - 1)
        top = 0
        bottom = r - 1
        wrapNext = false
        version++
    }

    /** The text of the screen, trailing spaces removed (tests, copy). */
    fun text(): String = lines.joinToString("\n") { row -> String(CharArray(row.size) { row[it].c }).trimEnd() }.trimEnd('\n')

    companion object {
        const val DEFAULT = -1
        const val RGB = 0x1000000
        const val BOLD = 1
        const val UNDERLINE = 2
        const val REVERSE = 4
        const val DIM = 8
        const val ITALIC = 16
        const val MAX_SCROLLBACK = 2000
        private const val S_GROUND = 0
        private const val S_ESC = 1
        private const val S_CSI = 2
        private const val S_OSC = 3
        private const val S_OSC_ESC = 4
        private const val S_CHARSET = 5
    }
}

/** What a key sends to the terminal ("" when it sends nothing). */
object TermKeys {
    fun arrow(dir: Char, appCursor: Boolean) = if (appCursor) "\u001bO$dir" else "\u001b[$dir"

    /** Ctrl with a character: Ctrl+a = 0x01 … Ctrl+z = 0x1a, Ctrl+[ = Esc, Ctrl+space = NUL. */
    fun ctrl(c: Char): String = when (val l = c.lowercaseChar()) {
        in 'a'..'z' -> (l.code - 'a'.code + 1).toChar().toString()
        '[', '3' -> "\u001b"
        '\\', '4' -> "\u001c"
        ']', '5' -> "\u001d"
        '^', '6' -> "\u001e"
        '_', '7', '/' -> "\u001f"
        ' ', '@', '2' -> "\u0000"
        '?', '8' -> "\u007f"
        else -> c.toString()
    }

    /** Named keys of the extra key row and the hardware keyboard. */
    fun named(name: String, appCursor: Boolean): String = when (name) {
        "esc" -> "\u001b"
        "tab" -> "\t"
        "enter" -> "\r"
        "backspace" -> "\u007f"
        "up" -> arrow('A', appCursor)
        "down" -> arrow('B', appCursor)
        "right" -> arrow('C', appCursor)
        "left" -> arrow('D', appCursor)
        "home" -> "\u001b[H"
        "end" -> "\u001b[F"
        "pgup" -> "\u001b[5~"
        "pgdn" -> "\u001b[6~"
        "delete" -> "\u001b[3~"
        else -> ""
    }
}
