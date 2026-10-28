package io.github.nathplay15.strataos.ui.surveillance

import io.github.nathplay15.strataos.net.manage.Segment
import java.time.LocalDate
import java.time.OffsetDateTime
import java.time.ZoneId

// The day timeline of the recordings: where the segments are, and which
// segment (and second in it) a point of the day falls in.

fun isoMillis(s: String): Long? = runCatching { OffsetDateTime.parse(s).toInstant().toEpochMilli() }.getOrNull()

/** Start of [day] ("2026-10-23") in [zone], in milliseconds. */
fun dayStart(day: String, zone: ZoneId = ZoneId.systemDefault()): Long? =
    runCatching { LocalDate.parse(day).atStartOfDay(zone).toInstant().toEpochMilli() }.getOrNull()

const val DAY_MS = 24 * 3600 * 1000L

/** A segment as a fraction [from, to] of the day (clipped to it). */
data class Span(val segment: Segment, val from: Float, val to: Float, val startMs: Long, val endMs: Long)

fun spans(segments: List<Segment>, dayStartMs: Long): List<Span> = segments.mapNotNull { s ->
    val a = isoMillis(s.start) ?: return@mapNotNull null
    val b = (isoMillis(s.end) ?: a).coerceAtLeast(a + 1000)
    val f = ((a - dayStartMs).toFloat() / DAY_MS).coerceIn(0f, 1f)
    val t = ((b - dayStartMs).toFloat() / DAY_MS).coerceIn(0f, 1f)
    if (t <= 0f || f >= 1f) null else Span(s, f, t, a, b)
}

/**
 * The segment playing at [fraction] of the day and the second to start at.
 * Between two segments, the next one from its start; after the last one,
 * nothing. A segment still being written cannot be played yet.
 */
fun segmentAt(spans: List<Span>, dayStartMs: Long, fraction: Float): Pair<Segment, Int>? {
    val t = dayStartMs + (fraction.coerceIn(0f, 1f) * DAY_MS).toLong()
    val playable = spans.filter { !it.segment.open }.sortedBy { it.startMs }
    playable.firstOrNull { t >= it.startMs && t < it.endMs }?.let { return it.segment to ((t - it.startMs) / 1000).toInt() }
    return playable.firstOrNull { it.startMs > t }?.let { it.segment to 0 }
}

/** mm:ss or h:mm:ss. */
fun clock(seconds: Int): String {
    val h = seconds / 3600
    val m = (seconds % 3600) / 60
    val s = seconds % 60
    return if (h > 0) "%d:%02d:%02d".format(h, m, s) else "%d:%02d".format(m, s)
}
