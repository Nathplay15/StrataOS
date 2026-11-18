package io.github.nathplay15.strataos.ui

import androidx.annotation.StringRes
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowLeft
import androidx.compose.material.icons.filled.Battery5Bar
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.SignalCellularAlt
import androidx.compose.material.icons.filled.Wifi
import androidx.compose.material3.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.FileEntry
import kotlinx.coroutines.launch

private class IntroPage(@StringRes val title: Int, @StringRes val text: Int, val screen: @Composable () -> Unit)

/** The first screens of the app: what it does, each with a picture of the app in a phone. */
@Composable
fun IntroCarousel(onDone: () -> Unit) {
    val pages = listOf(
        IntroPage(R.string.ui_intro1_title, R.string.ui_intro1_text) { MockFiles() },
        IntroPage(R.string.ui_intro2_title, R.string.ui_intro2_text) { MockStorage() },
        IntroPage(R.string.ui_intro3_title, R.string.ui_intro3_text) { MockPhotos() },
    )
    val pager = rememberPagerState { pages.size }
    val scope = rememberCoroutineScope()
    val last = pager.currentPage == pages.size - 1
    Box(Modifier.fillMaxSize().background(strata.page)) {
        // Soft shapes behind the pages.
        val tint = MaterialTheme.colorScheme.primary
        Canvas(Modifier.fillMaxSize()) {
            drawCircle(tint.copy(alpha = 0.07f), radius = size.width * 0.6f, center = Offset(size.width * 0.05f, size.height * 0.42f))
            drawCircle(tint.copy(alpha = 0.05f), radius = size.width * 0.45f, center = Offset(size.width * 1.0f, size.height * 0.12f))
        }
        Column(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing)) {
            HorizontalPager(pager, Modifier.weight(1f)) { i ->
                val p = pages[i]
                Column(Modifier.fillMaxSize().padding(horizontal = 28.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Spacer(Modifier.height(36.dp))
                    Text(stringResource(p.title), style = MaterialTheme.typography.headlineMedium, textAlign = TextAlign.Center)
                    Text(stringResource(p.text), Modifier.padding(top = 10.dp), style = MaterialTheme.typography.bodyLarge, textAlign = TextAlign.Center,
                        color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.height(28.dp))
                    PhoneFrame(Modifier.weight(1f)) { p.screen() }
                }
            }
            Row(Modifier.fillMaxWidth().padding(20.dp, 12.dp, 20.dp, 16.dp), verticalAlignment = Alignment.CenterVertically) {
                TextButton(onClick = onDone, modifier = Modifier.width(96.dp)) { if (!last) Text(stringResource(R.string.ui_skip)) }
                Row(Modifier.weight(1f), horizontalArrangement = Arrangement.Center) {
                    repeat(pages.size) { i ->
                        val on = pager.currentPage == i
                        val w by animateDpAsState(if (on) 22.dp else 8.dp, label = "dot")
                        val c by animateColorAsState(if (on) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outlineVariant, label = "dot")
                        Box(Modifier.padding(horizontal = 4.dp).height(8.dp).width(w).background(c, CircleShape))
                    }
                }
                Button(onClick = { if (last) onDone() else scope.launch { pager.animateScrollToPage(pager.currentPage + 1) } },
                    modifier = Modifier.width(132.dp), contentPadding = PaddingValues(horizontal = 12.dp)) {
                    Text(stringResource(if (last) R.string.ui_get_started else R.string.ui_next), maxLines = 1)
                }
            }
        }
    }
}

/** A phone drawn around [content]: rounded frame, a status bar, the bottom fading into the page. */
@Composable
private fun PhoneFrame(modifier: Modifier, content: @Composable () -> Unit) {
    val shape = RoundedCornerShape(topStart = 34.dp, topEnd = 34.dp)
    val bg = MaterialTheme.colorScheme.background
    BoxWithConstraints(modifier.fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
        Box(
            Modifier.width(minOf(maxWidth, 300.dp)).fillMaxHeight().shadow(18.dp, shape, ambientColor = MaterialTheme.colorScheme.primary, spotColor = MaterialTheme.colorScheme.primary)
                .clip(shape).background(MaterialTheme.colorScheme.surfaceContainerLowest)
                .border(5.dp, if (strata.dark) Color(0xFF3A4458) else Color(0xFFE3E8F2), shape),
        ) {
            Column(Modifier.fillMaxSize().padding(top = 5.dp, start = 5.dp, end = 5.dp)) {
                Row(Modifier.fillMaxWidth().padding(22.dp, 12.dp, 22.dp, 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("9:41", style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                    Icon(Icons.Default.Wifi, null, Modifier.size(14.dp))
                    Icon(Icons.Default.SignalCellularAlt, null, Modifier.size(14.dp))
                    Icon(Icons.Default.Battery5Bar, null, Modifier.size(14.dp))
                }
                content()
            }
            // The phone continues below the screen: fade it out.
            Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(90.dp).background(Brush.verticalGradient(listOf(bg.copy(alpha = 0f), bg))))
        }
    }
}

@Composable
private fun MockTitle(text: String, back: Boolean = false, sub: String? = null) {
    Column(Modifier.padding(16.dp, 4.dp, 16.dp, 8.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (back) Icon(Icons.AutoMirrored.Filled.KeyboardArrowLeft, null, Modifier.size(22.dp))
            Text(text, style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
            Icon(Icons.Default.Search, null, Modifier.size(20.dp))
        }
        if (sub != null) Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** Grey bars standing for text. */
@Composable
private fun Skeleton(widthFrac: Float, modifier: Modifier = Modifier) {
    Box(modifier.fillMaxWidth(widthFrac).height(6.dp).background(MaterialTheme.colorScheme.surfaceVariant, CircleShape))
}

@Composable
private fun MockFiles() {
    MockTitle(stringResource(R.string.tab_files), back = true)
    val folders = listOf(R.string.ui_mock_documents, R.string.a_photos, R.string.ui_mock_music)
    val files = listOf("a.pdf", "b.docx", "c.txt", "d.pptx", "e.xlsx", "f.zip")
    val cells: List<@Composable () -> Unit> = folders.map { f ->
        @Composable {
            FileTypeIcon(FileEntry("", "", true), Modifier.size(56.dp))
            Text(stringResource(f), style = MaterialTheme.typography.labelMedium, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 4.dp))
        }
    } + files.map { n ->
        @Composable {
            FileTypeIcon(FileEntry(n, n, false), Modifier.size(56.dp))
            Skeleton(0.7f, Modifier.padding(top = 8.dp))
        }
    }
    Column(Modifier.padding(horizontal = 8.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
        cells.chunked(3).forEach { row ->
            Row(Modifier.fillMaxWidth()) {
                row.forEach { c -> Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) { c() } }
            }
        }
    }
}

@Composable
private fun MockStorage() {
    MockTitle(stringResource(R.string.a_storage))
    Column(Modifier.padding(horizontal = 12.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        listOf(0.24f, 0.61f).forEachIndexed { i, used ->
            Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.background, RoundedCornerShape(16.dp)).padding(12.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    NasIllustration(Modifier.size(40.dp, 34.dp))
                    Column(Modifier.weight(1f).padding(start = 10.dp)) {
                        Text(stringResource(R.string.ui_mock_volume, i + 1), style = MaterialTheme.typography.titleSmall)
                        Row(Modifier.padding(top = 3.dp), verticalAlignment = Alignment.CenterVertically) {
                            Chip(stringResource(R.string.ui_healthy), Tone.OK)
                            Text(if (i == 0) "RAID 1" else "RAID 5", Modifier.padding(start = 8.dp), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
                ThinBar(used, Modifier.fillMaxWidth().padding(top = 12.dp, bottom = 4.dp))
                Text(formatBytes((used * 4e12).toLong()) + " / " + formatBytes(4_000_000_000_000), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
        Row(verticalAlignment = Alignment.CenterVertically) {
            listOf(true, true, true, false).forEach { ok ->
                Box(Modifier.padding(end = 8.dp).size(10.dp).background(if (ok) Tone.OK.color() else MaterialTheme.colorScheme.surfaceVariant, CircleShape))
            }
            Skeleton(0.6f, Modifier.padding(start = 4.dp))
        }
    }
}

/** The colours of the drawn pictures: sky, sun, land. */
private val scenes = listOf(
    Triple(Color(0xFF7DD3FC), Color(0xFFFDE047), Color(0xFF0EA5E9)),
    Triple(Color(0xFFFDBA74), Color(0xFFFFF7ED), Color(0xFFEA580C)),
    Triple(Color(0xFFC4B5FD), Color(0xFFFDF4FF), Color(0xFF7C3AED)),
    Triple(Color(0xFF86EFAC), Color(0xFFFEF9C3), Color(0xFF15803D)),
    Triple(Color(0xFFFDA4AF), Color(0xFFFFE4E6), Color(0xFFBE123C)),
    Triple(Color(0xFF93C5FD), Color(0xFFFFFFFF), Color(0xFF1E3A8A)),
)

@Composable
private fun MockPhotos() {
    MockTitle(stringResource(R.string.a_photos), sub = pluralStringResource(R.plurals.ui_items, 2481, 2481))
    Column {
        (0 until 15).chunked(3).forEach { row ->
            Row {
                row.forEach { i ->
                    val (sky, sun, land) = scenes[(i * 5 + i / 3) % scenes.size]
                    Canvas(Modifier.weight(1f).aspectRatio(1f).padding(0.5.dp)) {
                        drawRect(Brush.verticalGradient(listOf(sky, sky.copy(alpha = 0.6f))))
                        drawCircle(sun, radius = size.width * 0.12f, center = Offset(size.width * (0.25f + (i % 3) * 0.22f), size.height * 0.32f))
                        val hills = Path().apply {
                            moveTo(0f, size.height * 0.72f)
                            quadraticTo(size.width * 0.3f, size.height * (0.45f + (i % 4) * 0.05f), size.width * 0.6f, size.height * 0.68f)
                            quadraticTo(size.width * 0.8f, size.height * 0.55f, size.width, size.height * 0.66f)
                            lineTo(size.width, size.height); lineTo(0f, size.height); close()
                        }
                        drawPath(hills, land.copy(alpha = 0.85f))
                    }
                }
            }
        }
    }
}

/** The blue top of the connection screens, with the NAS drawn on it; the page continues as a light sheet. */
@Composable
fun OnboardingHeader(title: String, subtitle: String?) {
    StatusBarIcons(light = true)
    Box(Modifier.fillMaxWidth().background(strata.header)) {
        Canvas(Modifier.matchParentSize().clipToBounds()) {
            drawCircle(Color.White.copy(alpha = 0.09f), radius = size.width * 0.45f, center = Offset(size.width * 0.95f, 0f))
        }
        Column(Modifier.windowInsetsPadding(WindowInsets.statusBars).padding(24.dp, 20.dp, 24.dp, 52.dp)) {
            NasIllustration(Modifier.size(96.dp, 80.dp))
            Text(title, Modifier.padding(top = 18.dp), style = MaterialTheme.typography.headlineMedium, color = Color.White)
            if (subtitle != null) Text(subtitle, Modifier.padding(top = 6.dp), style = MaterialTheme.typography.bodyMedium, color = Color.White.copy(alpha = 0.88f), fontSize = 15.sp)
        }
        Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(28.dp).background(MaterialTheme.colorScheme.background, RoundedCornerShape(topStart = 28.dp, topEnd = 28.dp)))
    }
}
