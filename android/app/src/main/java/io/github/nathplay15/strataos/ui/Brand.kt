package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.horizontalScroll
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material3.Card
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.windowInsetsTopHeight
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.nathplay15.strataos.net.FileEntry

/** A rounded square in the colours of [paint] with a white [icon]: the app grid, the Me list, the cards. */
@Composable
fun TileIcon(icon: ImageVector, paint: TilePaint, size: Dp = 56.dp, iconSize: Dp = size * 0.5f) {
    Box(
        Modifier.size(size).shadow(if (size >= 48.dp) 6.dp else 0.dp, RoundedCornerShape(size * 0.32f), ambientColor = paint.to, spotColor = paint.to)
            .clip(RoundedCornerShape(size * 0.32f)).background(paint.brush),
        contentAlignment = Alignment.Center,
    ) {
        // A soft highlight in the top left corner gives the tile some depth.
        Canvas(Modifier.matchParentSize()) {
            drawCircle(Color.White.copy(alpha = 0.18f), radius = this.size.width * 0.55f, center = Offset(this.size.width * 0.15f, this.size.height * 0.05f))
        }
        Icon(icon, null, Modifier.size(iconSize), tint = Color.White)
    }
}

/** A labelled tile of the app grid, with an optional badge (count or short word). */
@Composable
fun AppTile(icon: ImageVector, label: String, paint: TilePaint, modifier: Modifier = Modifier, badge: String? = null, onClick: () -> Unit) {
    Column(
        modifier.clip(MaterialTheme.shapes.medium).clickable(onClick = onClick).padding(vertical = 8.dp, horizontal = 2.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Box {
            TileIcon(icon, paint)
            if (badge != null) Text(
                badge, color = Color.White, fontSize = 10.sp, fontWeight = FontWeight.Bold, maxLines = 1,
                modifier = Modifier.align(Alignment.TopEnd).offset(8.dp, (-6).dp).background(MaterialTheme.colorScheme.error, CircleShape).padding(horizontal = 6.dp, vertical = 1.dp),
            )
        }
        Text(
            label, Modifier.padding(top = 8.dp), style = MaterialTheme.typography.labelMedium, textAlign = TextAlign.Center,
            maxLines = 2, overflow = TextOverflow.Ellipsis, color = MaterialTheme.colorScheme.onSurface,
        )
    }
}

/** A white card with a title row (and an optional trailing action) above its content. */
@Composable
fun SectionCard(title: String, modifier: Modifier = Modifier, trailing: (@Composable RowScope.() -> Unit)? = null, onClick: (() -> Unit)? = null, content: @Composable () -> Unit) {
    Card(modifier.fillMaxWidth().let { if (onClick != null) it.clip(MaterialTheme.shapes.medium).clickable(onClick = onClick) else it }) {
        Column(Modifier.padding(18.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(title, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                trailing?.invoke(this)
            }
            content()
        }
    }
}

/** A row of a settings-like list: coloured icon, title, optional subtitle and a chevron. */
@Composable
fun NavRow(icon: ImageVector, paint: TilePaint, title: String, subtitle: String? = null, trailing: (@Composable () -> Unit)? = null, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = 16.dp, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
        TileIcon(icon, paint, size = 36.dp, iconSize = 20.dp)
        Column(Modifier.weight(1f).padding(start = 14.dp)) {
            Text(title, style = MaterialTheme.typography.bodyLarge)
            if (!subtitle.isNullOrEmpty()) Text(subtitle, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        if (trailing != null) trailing()
        else Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** A card grouping [NavRow]s, with thin dividers between them. */
@Composable
fun NavGroup(title: String? = null, rows: List<@Composable () -> Unit>) {
    Column {
        if (title != null) Text(title.uppercase(), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 6.dp, bottom = 8.dp))
        Card(Modifier.fillMaxWidth()) {
            rows.forEachIndexed { i, r ->
                if (i > 0) HorizontalDivider(Modifier.padding(start = 66.dp), color = MaterialTheme.colorScheme.outlineVariant)
                r()
            }
        }
    }
}

/**
 * The NAS itself, drawn: a dark box with four drive bays, their activity
 * lights and a power button. [healthy] turns the lights green or amber.
 */
@Composable
fun NasIllustration(modifier: Modifier = Modifier, healthy: Boolean = true) {
    Canvas(modifier) {
        val w = size.width
        val h = size.height
        val r = w * 0.09f
        // Shadow under the box.
        drawOval(Color.Black.copy(alpha = 0.18f), Offset(w * 0.08f, h * 0.9f), Size(w * 0.84f, h * 0.1f))
        // Body, lit from the top.
        drawRoundRect(Brush.verticalGradient(listOf(Color(0xFF4A5468), Color(0xFF232A38)), 0f, h * 0.92f), Offset(0f, 0f), Size(w, h * 0.92f), CornerRadius(r))
        drawRoundRect(Color.White.copy(alpha = 0.16f), Offset(w * 0.02f, h * 0.015f), Size(w * 0.96f, h * 0.06f), CornerRadius(r))
        // Front panel.
        val px = w * 0.08f
        val py = h * 0.12f
        val pw = w * 0.84f
        val ph = h * 0.68f
        drawRoundRect(Color(0xFF151A24), Offset(px, py), Size(pw, ph), CornerRadius(r * 0.6f))
        val bays = 4
        val gap = pw * 0.035f
        val bw = (pw - gap * (bays + 1)) / bays
        for (i in 0 until bays) {
            val bx = px + gap + i * (bw + gap)
            drawRoundRect(Brush.verticalGradient(listOf(Color(0xFF5B6476), Color(0xFF394152)), py + gap, py + ph - gap), Offset(bx, py + gap), Size(bw, ph - gap * 2), CornerRadius(bw * 0.18f))
            // Grip lines.
            for (j in 0 until 3) {
                val ly = py + ph * 0.25f + j * ph * 0.08f
                drawLine(Color.Black.copy(alpha = 0.35f), Offset(bx + bw * 0.25f, ly), Offset(bx + bw * 0.75f, ly), strokeWidth = bw * 0.07f)
            }
            drawCircle(if (healthy) Color(0xFF4ADE80) else Color(0xFFFBBF24), radius = bw * 0.09f, center = Offset(bx + bw / 2, py + ph * 0.82f))
        }
        // Power button.
        drawCircle(Color(0xFF60A5FA), radius = w * 0.022f, center = Offset(w * 0.88f, h * 0.86f))
    }
}

/** Colour and short label of a file type, as shown on its icon. */
private data class FileType(val color: Color, val label: String)

private fun fileType(name: String): FileType {
    val ext = name.substringAfterLast('.', "").lowercase()
    val label = ext.uppercase().take(4)
    return when (ext) {
        "doc", "docx", "odt", "rtf", "pages" -> FileType(Color(0xFF2F6BFF), label)
        "xls", "xlsx", "ods", "csv", "numbers" -> FileType(Color(0xFF16A34A), label)
        "ppt", "pptx", "odp", "key" -> FileType(Color(0xFFF97316), label)
        "pdf" -> FileType(Color(0xFFE5383B), label)
        "txt", "md", "log", "json", "xml", "yml", "yaml", "ini", "conf" -> FileType(Color(0xFF10B981), label)
        "zip", "tar", "gz", "tgz", "7z", "rar", "xz", "bz2", "zst" -> FileType(Color(0xFFD97706), label)
        "apk", "deb", "iso", "img", "exe", "dmg" -> FileType(Color(0xFF64748B), label)
        else -> when (kindOf(name)) {
            Kind.IMAGE -> FileType(Color(0xFFEC4899), label)
            Kind.VIDEO -> FileType(Color(0xFF8B5CF6), label)
            Kind.AUDIO -> FileType(Color(0xFF06B6D4), label)
            else -> FileType(Color(0xFF94A3B8), label)
        }
    }
}

/** A big, colourful icon of a file: a folder, or a page with its type written on a coloured band. */
@Composable
fun FileTypeIcon(e: FileEntry, modifier: Modifier = Modifier) {
    if (e.dir) {
        Box(modifier, contentAlignment = Alignment.Center) {
            FolderShape(Modifier.matchParentSize())
            if (e.encrypted) Icon(Icons.Default.Lock, null, Modifier.size(18.dp).offset(y = 3.dp), tint = Color.White)
        }
        return
    }
    val t = fileType(e.name)
    val page = MaterialTheme.colorScheme.surfaceContainerLowest
    BoxWithConstraints(modifier, contentAlignment = Alignment.Center) {
        val small = maxWidth < 64.dp
        Canvas(Modifier.matchParentSize()) {
            // The page fits in the square, a little narrower than tall.
            val ph = size.height * 0.92f
            val pw = ph * 0.78f
            val x = (size.width - pw) / 2
            val y = (size.height - ph) / 2
            val fold = pw * 0.3f
            val sheet = Path().apply {
                moveTo(x + pw * 0.12f, y)
                lineTo(x + pw - fold, y)
                lineTo(x + pw, y + fold)
                lineTo(x + pw, y + ph - pw * 0.12f)
                quadraticTo(x + pw, y + ph, x + pw - pw * 0.12f, y + ph)
                lineTo(x + pw * 0.12f, y + ph)
                quadraticTo(x, y + ph, x, y + ph - pw * 0.12f)
                lineTo(x, y + pw * 0.12f)
                quadraticTo(x, y, x + pw * 0.12f, y)
                close()
            }
            drawPath(sheet, page)
            drawPath(sheet, t.color.copy(alpha = 0.55f), style = Stroke(size.width * 0.03f))
            val corner = Path().apply { moveTo(x + pw - fold, y); lineTo(x + pw - fold, y + fold * 0.8f); quadraticTo(x + pw - fold, y + fold, x + pw - fold * 0.8f, y + fold); lineTo(x + pw, y + fold); close() }
            drawPath(corner, t.color.copy(alpha = 0.35f))
            // Text lines.
            for (i in 0 until 2) {
                val ly = y + ph * (0.3f + i * 0.12f)
                drawLine(t.color.copy(alpha = 0.35f), Offset(x + pw * 0.2f, ly), Offset(x + pw * (if (i == 0) 0.62f else 0.8f), ly), strokeWidth = size.width * 0.04f)
            }
        }
        if (t.label.isNotEmpty()) {
            Text(
                t.label, color = Color.White, fontWeight = FontWeight.Bold, maxLines = 1, fontSize = if (small) 7.sp else 11.sp,
                modifier = Modifier.align(Alignment.BottomStart).padding(bottom = if (small) 6.dp else 12.dp)
                    .background(t.color, RoundedCornerShape(if (small) 3.dp else 5.dp)).padding(horizontal = if (small) 3.dp else 6.dp, vertical = 0.dp),
            )
        }
    }
}

/** The folder icon: back flap and front in the brand blue. */
@Composable
private fun FolderShape(modifier: Modifier) {
    val dark = strata.dark
    Canvas(modifier) {
        val w = size.width
        val h = size.height
        val top = h * 0.16f
        val bodyH = h * 0.68f
        val r = w * 0.08f
        val back = if (dark) Color(0xFF3B6FD8) else Color(0xFF7FB0FF)
        val front = Brush.verticalGradient(listOf(if (dark) Color(0xFF5B8DF0) else Color(0xFF9CC3FF), if (dark) Color(0xFF3F74E0) else Color(0xFF6AA2FF)), top + h * 0.12f, top + bodyH)
        val tab = Path().apply {
            moveTo(w * 0.06f + r, top)
            lineTo(w * 0.38f, top)
            lineTo(w * 0.46f, top + h * 0.08f)
            lineTo(w * 0.94f - r, top + h * 0.08f)
            quadraticTo(w * 0.94f, top + h * 0.08f, w * 0.94f, top + h * 0.08f + r)
            lineTo(w * 0.94f, top + bodyH)
            lineTo(w * 0.06f, top + bodyH)
            lineTo(w * 0.06f, top + r)
            quadraticTo(w * 0.06f, top, w * 0.06f + r, top)
            close()
        }
        drawPath(tab, back)
        drawRoundRect(front, Offset(w * 0.06f, top + h * 0.15f), Size(w * 0.88f, bodyH - h * 0.15f), CornerRadius(r))
        drawRoundRect(Color.White.copy(alpha = 0.22f), Offset(w * 0.06f, top + h * 0.15f), Size(w * 0.88f, h * 0.025f), CornerRadius(r))
    }
}

/** Behind the status bar once a screen with a coloured header scrolls under it, so the clock stays readable. */
@Composable
fun StatusBarScrim(visible: Boolean) {
    val alpha by androidx.compose.animation.core.animateFloatAsState(if (visible) 1f else 0f, label = "scrim")
    Box(
        Modifier.fillMaxWidth().windowInsetsTopHeight(androidx.compose.foundation.layout.WindowInsets.statusBars)
            .background(MaterialTheme.colorScheme.background.copy(alpha = alpha)),
    )
}

/** White status bar icons while [light] (a blue header is under them), the theme's own otherwise. */
@Composable
fun StatusBarIcons(light: Boolean) {
    val view = androidx.compose.ui.platform.LocalView.current
    val dark = strata.dark
    androidx.compose.runtime.DisposableEffect(light, dark) {
        val window = (view.context as? android.app.Activity)?.window
        val c = window?.let { androidx.core.view.WindowCompat.getInsetsController(it, view) }
        c?.isAppearanceLightStatusBars = !light && !dark
        onDispose { c?.isAppearanceLightStatusBars = !dark }
    }
}

/**
 * The top of every screen: the brand gradient with a white title, the back
 * arrow, actions and optional tabs, ending on the rounded top of the page,
 * like the Home and Me headers. [navIcon] null: no navigation button.
 */
@Composable
fun BrandTopBar(
    title: String,
    onNav: (() -> Unit)? = null,
    subtitle: String? = null,
    navIcon: ImageVector = Icons.AutoMirrored.Filled.ArrowBack,
    actions: @Composable RowScope.() -> Unit = {},
    tabs: List<String> = emptyList(),
    tab: Int = 0,
    onTab: (Int) -> Unit = {},
) {
    StatusBarIcons(light = true)
    androidx.compose.runtime.CompositionLocalProvider(androidx.compose.material3.LocalContentColor provides Color.White) {
        Column(Modifier.fillMaxWidth().background(strata.header).windowInsetsPadding(androidx.compose.foundation.layout.WindowInsets.statusBars)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 60.dp).padding(start = if (onNav == null) 16.dp else 4.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                if (onNav != null) androidx.compose.material3.IconButton(onClick = onNav) { Icon(navIcon, androidx.compose.ui.res.stringResource(io.github.nathplay15.strataos.R.string.back)) }
                Column(Modifier.weight(1f).padding(start = 4.dp, end = 4.dp)) {
                    Text(title, style = MaterialTheme.typography.titleLarge, color = Color.White, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (!subtitle.isNullOrEmpty()) Text(subtitle, style = MaterialTheme.typography.bodySmall, color = Color.White.copy(alpha = 0.8f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                // Text buttons of the bar are white like its icons (not the blue of the theme).
                MaterialTheme(colorScheme = MaterialTheme.colorScheme.copy(primary = Color.White)) { actions() }
            }
            if (tabs.isNotEmpty()) {
                val st = androidx.compose.foundation.rememberScrollState()
                Row(Modifier.fillMaxWidth().horizontalScroll(st).padding(start = 12.dp, end = 12.dp, bottom = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    tabs.forEachIndexed { i, t ->
                        val sel = i == tab
                        Text(
                            t, style = MaterialTheme.typography.labelLarge, color = if (sel) Color(0xFF1456F0) else Color.White, maxLines = 1,
                            modifier = Modifier.clip(CircleShape).background(if (sel) Color.White else Color.White.copy(alpha = 0.16f))
                                .clickable(role = androidx.compose.ui.semantics.Role.Tab) { onTab(i) }.padding(horizontal = 14.dp, vertical = 8.dp),
                        )
                    }
                }
            }
            Box(Modifier.fillMaxWidth().padding(top = 10.dp).height(18.dp).background(MaterialTheme.colorScheme.background, RoundedCornerShape(topStart = 22.dp, topEnd = 22.dp)))
        }
    }
}
