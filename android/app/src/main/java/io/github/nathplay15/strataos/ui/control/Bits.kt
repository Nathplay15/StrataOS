package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.Tone
import kotlinx.serialization.json.JsonObject
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle

// Small pieces shared by the Control panel screens.

/** A date of the NAS (RFC 3339, or Unix seconds) in the phone's short format; "" when absent. */
fun whenText(v: String): String {
    if (v.isBlank() || v.startsWith("0001-")) return ""
    val t = runCatching { OffsetDateTime.parse(v).toInstant() }.getOrNull()
        ?: v.toLongOrNull()?.let { Instant.ofEpochSecond(if (it > 100_000_000_000L) it / 1_000_000 else it) }
        ?: return v
    return DateTimeFormatter.ofLocalizedDateTime(FormatStyle.SHORT).withZone(ZoneId.systemDefault()).format(t)
}

/** The badge of the last run of a task (history is newest first). */
@Composable
fun RunBadge(last: JsonObject?, running: Boolean) {
    when {
        running -> StatusBadge(stringResource(R.string.ctl_running), Tone.Accent)
        last == null -> StatusBadge(stringResource(R.string.ctl_never_run), Tone.Neutral)
        last.s("status") == "success" -> StatusBadge(stringResource(R.string.ctl_ok), Tone.Ok)
        last.s("status") == "stopped" -> StatusBadge(stringResource(R.string.ctl_stopped), Tone.Warn)
        else -> StatusBadge(stringResource(R.string.ctl_failed), Tone.Bad)
    }
}

/** The tone of a level of the NAS (info, warning, error; high, medium, low). */
fun toneOf(level: String): Tone = when (level) {
    "error", "high", "block", "failed", "danger" -> Tone.Bad
    "warning", "warn", "medium" -> Tone.Warn
    "ok", "success" -> Tone.Ok
    "low" -> Tone.Accent
    else -> Tone.Neutral
}

/** A title, an optional second line and a badge on the right: the head of a card. */
@Composable
fun CardHead(title: String, sub: String? = null, badge: (@Composable () -> Unit)? = null) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.titleMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (!sub.isNullOrEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        badge?.invoke()
    }
}

/** Grey help text under a card or a list. */
@Composable
fun Hint(text: String) {
    Text(text, Modifier.padding(horizontal = 4.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
}
