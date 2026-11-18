package io.github.nathplay15.strataos.ui

import android.content.Context
import android.content.Intent
import androidx.core.net.toUri
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Apps
import androidx.compose.material.icons.filled.AutoAwesome
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.CloudOff
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.Group
import androidx.compose.material.icons.filled.MonitorHeart
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material.icons.filled.SwapVert
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material.icons.filled.VpnKey
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.edit
import androidx.work.WorkInfo
import androidx.work.WorkManager
import androidx.work.WorkQuery
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.backup.BackupSettings
import io.github.nathplay15.strataos.net.Events
import io.github.nathplay15.strataos.net.NasEvent
import io.github.nathplay15.strataos.net.Overview
import io.github.nathplay15.strataos.net.PhotoPage
import io.github.nathplay15.strataos.net.Stack
import io.github.nathplay15.strataos.net.StatsHistory
import io.github.nathplay15.strataos.net.SystemInfo
import io.github.nathplay15.strataos.net.manage.PublishedLink
import io.github.nathplay15.strataos.net.manage.publishedApps
import io.github.nathplay15.strataos.net.manage.remoteState
import io.github.nathplay15.strataos.net.photoThumbUrl
import io.github.nathplay15.strataos.net.photos
import io.github.nathplay15.strataos.net.stacks
import io.github.nathplay15.strataos.ui.web.AppWebView
import kotlinx.coroutines.flow.map

private data class HomeData(
    val info: SystemInfo, val overview: Overview, val stats: StatsHistory?, val events: Events?,
    val apps: List<PublishedLink>, val stacks: List<Stack>, val photos: PhotoPage?,
)

/** What is moving between the phone and the NAS right now. */
private data class Transfers(val uploads: Int, val backup: Boolean, val sync: Boolean) {
    val any get() = uploads > 0 || backup || sync
}

private fun uiPrefs(ctx: Context) = ctx.getSharedPreferences("home_ui", Context.MODE_PRIVATE)

/**
 * The Home tab: a blue header (search, transfers, notifications, the NAS),
 * the grid of the places of the app, then cards about the NAS — photos,
 * storage, applications, activity and the latest notifications.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(go: HomeNav) {
    val ctx = LocalContext.current
    val admin = Graph.prefs.server?.isAdmin == true
    val loader = rememberLoad(Unit) {
        val a = api()
        HomeData(
            a.systemInfo(), a.overview(),
            runCatching { a.stats() }.getOrNull(),
            if (admin) runCatching { a.events(0) }.getOrNull() else null,
            if (admin) runCatching { publishedApps(a.remoteState()) }.getOrDefault(emptyList()) else emptyList(),
            if (admin) runCatching { a.stacks() }.getOrDefault(emptyList()) else emptyList(),
            runCatching { a.photos(0, 4) }.getOrNull(),
        )
    }
    val transfers by remember {
        WorkManager.getInstance(ctx).getWorkInfosFlow(WorkQuery.fromTags("upload", "backup", "sync")).map { infos ->
            val running = infos.filter { it.state == WorkInfo.State.RUNNING || (it.state == WorkInfo.State.ENQUEUED && "upload" in it.tags) }
            Transfers(running.count { "upload" in it.tags }, running.any { "backup" in it.tags }, running.any { "sync" in it.tags })
        }
    }.collectAsState(Transfers(0, false, false))
    var seen by remember { mutableLongStateOf(uiPrefs(ctx).getLong("seenEvent", 0)) }
    var web by remember { mutableStateOf<Pair<String, String>?>(null) }
    var nasSheet by remember { mutableStateOf(false) }
    var transfersSheet by remember { mutableStateOf(false) }
    web?.let { (name, url) ->
        AppWebView(name, url) { web = null }
        return
    }
    val d = loader.value
    val events = d?.events?.events.orEmpty()
    val unread = events.count { it.id > seen }
    fun openNotifications() {
        events.maxOfOrNull { it.id }?.let { seen = it; uiPrefs(ctx).edit { putLong("seenEvent", it) } }
        go.section(STATUS)
    }

    PullToRefreshBox(isRefreshing = loader.refreshing, onRefresh = loader.reload, modifier = Modifier.fillMaxSize()) {
        val list = androidx.compose.foundation.lazy.rememberLazyListState()
        LazyColumn(Modifier.fillMaxSize(), state = list, contentPadding = PaddingValues(bottom = 24.dp)) {
            item {
                HomeHeader(
                    hostname = d?.info?.hostname ?: Graph.prefs.server?.hostname.orEmpty(),
                    state = loader.state, alerts = d?.overview?.alerts?.size ?: 0, unread = unread, transfersActive = transfers.any,
                    onSearch = go.search, onTransfers = { transfersSheet = true }, onBell = ::openNotifications, onNas = { nasSheet = true },
                )
            }
            item { PlacesGrid(admin, go) }
            item { BackupCard(ctx) { go.overlay("backup") } }
            when (val st = loader.state) {
                Loaded.Loading -> item { Box(Modifier.fillMaxWidth().padding(32.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator() } }
                is Loaded.Failed -> item {
                    Card(Modifier.fillMaxWidth().padding(16.dp, 6.dp)) {
                        Column(Modifier.padding(18.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(Icons.Default.CloudOff, null, tint = MaterialTheme.colorScheme.error)
                                Text(stringResource(R.string.ui_offline), Modifier.padding(start = 10.dp), style = MaterialTheme.typography.titleMedium)
                            }
                            Text(errorText(ctx, st.error), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            OutlinedButton(onClick = loader.reload) { Text(stringResource(R.string.retry)) }
                        }
                    }
                }
                is Loaded.Ok -> {
                    val v = st.value
                    v.photos?.let { p -> item { PhotosCard(p) { go.tab("photos") } } }
                    if (v.overview.volumes.isNotEmpty()) item { StorageCard(v.overview) { go.section(if (admin) "storage" else STATUS) } }
                    if (v.stacks.isNotEmpty()) item { StacksCard(v.stacks) { go.section("apps") } }
                    if (v.apps.isNotEmpty()) item { AppsCard(v.apps, onOpen = { name, url -> web = name to url }) }
                    if (v.stats != null && v.stats.history.size >= 2) item { ActivityCard(v.stats) }
                    if (v.events?.events?.isNotEmpty() == true) item { NotificationsCard(v.events.events, ::openNotifications) }
                }
            }
        }
        val scrolled by remember { androidx.compose.runtime.derivedStateOf { list.firstVisibleItemIndex > 0 } }
        StatusBarScrim(scrolled)
        StatusBarIcons(light = !scrolled)
    }

    if (nasSheet) ModalBottomSheet(onDismissRequest = { nasSheet = false }) {
        NasSheet(d, onStatus = { nasSheet = false; go.section(STATUS) }, onSettings = { nasSheet = false; go.overlay("settings") })
    }
    if (transfersSheet) ModalBottomSheet(onDismissRequest = { transfersSheet = false }) {
        TransfersSheet(transfers, BackupSettings(ctx).enabled, onBackup = { transfersSheet = false; go.overlay("backup") }, onSync = { transfersSheet = false; go.overlay("sync") })
    }
}

/** The blue header: search, transfers, notifications, then the NAS name and picture. Its bottom is the top of the sheet. */
@Composable
private fun HomeHeader(
    hostname: String, state: Loaded<*>, alerts: Int, unread: Int, transfersActive: Boolean,
    onSearch: () -> Unit, onTransfers: () -> Unit, onBell: () -> Unit, onNas: () -> Unit,
) {
    val white = Color.White
    Box(Modifier.fillMaxWidth().background(strata.header)) {
        // Two large soft circles: light on the blue, nothing to read.
        Canvas(Modifier.matchParentSize().clipToBounds()) {
            drawCircle(white.copy(alpha = 0.09f), radius = size.width * 0.42f, center = Offset(size.width * 0.95f, size.height * 0.05f))
            drawCircle(white.copy(alpha = 0.06f), radius = size.width * 0.3f, center = Offset(size.width * 0.05f, size.height * 0.95f))
        }
        Column(Modifier.windowInsetsPadding(WindowInsets.statusBars).padding(start = 16.dp, top = 10.dp, end = 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Row(
                    Modifier.weight(1f).height(46.dp).clip(CircleShape).background(white.copy(alpha = 0.16f))
                        .border(1.dp, white.copy(alpha = 0.35f), CircleShape).clickable(onClick = onSearch).padding(horizontal = 16.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Icon(Icons.Default.Search, null, Modifier.size(22.dp), tint = white)
                    Text(stringResource(R.string.ui_search_files), Modifier.padding(start = 10.dp), color = white.copy(alpha = 0.9f), style = MaterialTheme.typography.bodyLarge)
                }
                HeaderButton(Icons.Default.SwapVert, stringResource(R.string.ui_transfers), if (transfersActive) "" else null, onTransfers)
                HeaderButton(Icons.Default.Notifications, stringResource(R.string.notifications), if (unread > 0) (if (unread > 99) "99+" else unread.toString()) else null, onBell)
            }
            Row(
                Modifier.padding(top = 14.dp, end = 8.dp).clip(MaterialTheme.shapes.medium).clickable(onClickLabel = stringResource(R.string.ui_nas_details), onClick = onNas).padding(vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Column(Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(hostname, style = MaterialTheme.typography.headlineSmall, color = white, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, Modifier.size(28.dp), tint = white)
                    }
                    Row(Modifier.padding(top = 6.dp).background(white.copy(alpha = 0.16f), CircleShape).padding(horizontal = 10.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        val (icon, text) = when {
                            state is Loaded.Failed -> Icons.Default.CloudOff to stringResource(R.string.ui_offline)
                            state is Loaded.Loading -> null to "…"
                            alerts > 0 -> Icons.Default.Warning to stringResource(R.string.a_alerts_n, alerts)
                            else -> Icons.Default.CheckCircle to stringResource(R.string.status_all_good)
                        }
                        if (icon != null) Icon(icon, null, Modifier.size(16.dp).padding(end = 0.dp), tint = white)
                        Text(text, Modifier.padding(start = 6.dp), color = white, style = MaterialTheme.typography.labelLarge)
                    }
                }
                NasIllustration(Modifier.size(112.dp, 92.dp), healthy = alerts == 0 && state !is Loaded.Failed)
            }
            Spacer(Modifier.height(40.dp))
        }
        // The light sheet of the page starts here, over the blue.
        Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(26.dp).background(MaterialTheme.colorScheme.background, RoundedCornerShape(topStart = 26.dp, topEnd = 26.dp)))
    }
}

@Composable
private fun HeaderButton(icon: ImageVector, label: String, badge: String?, onClick: () -> Unit) {
    Box(Modifier.size(48.dp).clip(CircleShape).clickable(onClickLabel = label, onClick = onClick), contentAlignment = Alignment.Center) {
        Icon(icon, label, Modifier.size(26.dp), tint = Color.White)
        if (badge != null) Box(
            Modifier.align(Alignment.TopEnd).offset((-6).dp, 8.dp).let { if (badge.isEmpty()) it.size(10.dp) else it }
                .background(Color(0xFFFF4D4F), CircleShape).border(1.5.dp, Color.White, CircleShape),
            contentAlignment = Alignment.Center,
        ) {
            if (badge.isNotEmpty()) Text(badge, color = Color.White, fontSize = 10.sp, fontWeight = FontWeight.Bold, modifier = Modifier.padding(horizontal = 5.dp))
        }
    }
}

/** A place of the app on the Home grid. */
private class Place(val icon: ImageVector, val label: Int, val paint: TilePaint, val open: () -> Unit)

@Composable
private fun PlacesGrid(admin: Boolean, go: HomeNav) {
    val places = buildList {
        add(Place(Icons.Default.Folder, R.string.tab_files, Tiles.files) { go.tab("files") })
        add(Place(Icons.Default.PhotoLibrary, R.string.a_photos, Tiles.photos) { go.tab("photos") })
        add(Place(Icons.Default.MonitorHeart, R.string.tab_status, Tiles.status) { go.section(STATUS) })
        if (admin) add(Place(Icons.Default.Storage, R.string.a_storage, Tiles.storage) { go.section("storage") })
        add(Place(Icons.Default.Backup, R.string.tab_backup, Tiles.backup) { go.overlay("backup") })
        add(Place(Icons.Default.Sync, R.string.sync_title, Tiles.sync) { go.overlay("sync") })
        if (admin) add(Place(Icons.Default.Apps, R.string.a_applications, Tiles.apps) { go.section("apps") })
        add(Place(Icons.Default.AutoAwesome, R.string.a_assistant, Tiles.assistant) { go.tab("assistant") })
        if (admin) add(Place(Icons.Default.Computer, R.string.area_vms, Tiles.vms) { go.section("vms") })
        add(Place(Icons.Default.VpnKey, R.string.area_vpn, Tiles.vpn) { go.section("vpn") })
        if (admin) add(Place(Icons.Default.Group, R.string.area_users, Tiles.users) { go.section("users") })
        add(Place(Icons.Default.GridView, R.string.ui_more, Tiles.more) { go.tab("manage") })
    }
    Column(Modifier.padding(horizontal = 10.dp).padding(bottom = 6.dp)) {
        places.chunked(4).forEach { row ->
            Row(Modifier.fillMaxWidth()) {
                row.forEach { p -> AppTile(p.icon, stringResource(p.label), p.paint, Modifier.weight(1f), onClick = p.open) }
                repeat(4 - row.size) { Spacer(Modifier.weight(1f)) }
            }
        }
    }
}

/** The photo backup of this phone: what it does, and a pill to go there. */
@Composable
private fun BackupCard(ctx: Context, onOpen: () -> Unit) {
    val on = remember { BackupSettings(ctx).enabled }
    Card(
        Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp).clip(MaterialTheme.shapes.medium).clickable(onClick = onOpen),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.primaryContainer.copy(alpha = if (strata.dark) 0.35f else 0.55f)),
    ) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            TileIcon(Icons.Default.Backup, Tiles.backup, size = 48.dp)
            Column(Modifier.weight(1f).padding(horizontal = 14.dp)) {
                Text(stringResource(R.string.channel_backup), style = MaterialTheme.typography.titleMedium)
                Text(stringResource(if (on) R.string.ui_backup_card_on else R.string.ui_backup_card_off), style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            Button(onClick = onOpen, contentPadding = PaddingValues(horizontal = 22.dp)) { Text(stringResource(R.string.ui_go)) }
        }
    }
}

/** The latest photos of the NAS, in a row; opens the Photos tab. */
@Composable
private fun PhotosCard(p: PhotoPage, onOpen: () -> Unit) {
    SectionCard(stringResource(R.string.a_photos), Modifier.padding(horizontal = 16.dp, vertical = 6.dp), onClick = onOpen, trailing = {
        Text(
            if (p.installed) pluralStringResource(R.plurals.ui_items, p.total, p.total) else stringResource(R.string.ui_not_installed),
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }) {
        if (p.items.isNotEmpty()) Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            val shown = p.items.take(4)
            shown.forEach { ph ->
                val url = api().photoThumbUrl(ph.path, "s").newBuilder().addQueryParameter("v", ph.mtime.toString()).build()
                NasImage(url, null, Modifier.weight(1f).aspectRatio(1f).clip(RoundedCornerShape(14.dp)), maxPx = 300, fallback = Icons.Default.PhotoLibrary)
            }
            repeat(4 - shown.size) { Spacer(Modifier.weight(1f)) }
        }
    }
}

/** The Docker stacks of the NAS with their health; opens the App Center. */
@Composable
private fun StacksCard(stacks: List<Stack>, onOpen: () -> Unit) {
    SectionCard(stringResource(R.string.home_apps), Modifier.padding(horizontal = 16.dp, vertical = 6.dp), onClick = onOpen) {
        stacks.forEach { s ->
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                TileIcon(Icons.Default.Apps, Tiles.apps, size = 32.dp, iconSize = 18.dp)
                Text(s.name, Modifier.weight(1f).padding(start = 12.dp), style = MaterialTheme.typography.bodyLarge, maxLines = 1)
                Text(stringResource(R.string.a_stack_counts, s.running, s.services), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Spacer(Modifier.width(8.dp))
                Chip(statusLabel(s.status), toneOf(s.status))
            }
        }
    }
}

@Composable
private fun statusLabel(status: String): String = when (status.lowercase()) {
    "running" -> stringResource(R.string.state_running)
    "partial" -> stringResource(R.string.state_partial)
    "stopped" -> stringResource(R.string.state_stopped)
    else -> status
}

/** The published apps of the NAS as a launcher grid: a tap opens in the app, a long press in the browser. */
@Composable
private fun AppsCard(apps: List<PublishedLink>, onOpen: (String, String) -> Unit) {
    val ctx = LocalContext.current
    SectionCard(stringResource(R.string.a_my_apps), Modifier.padding(horizontal = 16.dp, vertical = 6.dp)) {
        apps.chunked(4).forEach { row ->
            Row(Modifier.fillMaxWidth()) {
                row.forEach { a ->
                    LinkTile(a, Modifier.weight(1f), onOpen = { onOpen(a.name, a.url) },
                        onBrowser = { runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, a.url.toUri())) } })
                }
                repeat(4 - row.size) { Spacer(Modifier.weight(1f)) }
            }
        }
    }
}

@OptIn(androidx.compose.foundation.ExperimentalFoundationApi::class)
@Composable
private fun LinkTile(a: PublishedLink, modifier: Modifier, onOpen: () -> Unit, onBrowser: () -> Unit) {
    Column(
        modifier.clip(MaterialTheme.shapes.medium).combinedClickable(onClick = onOpen, onLongClick = onBrowser).padding(vertical = 6.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Surface(shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.7f), modifier = Modifier.size(52.dp)) {
            Box(contentAlignment = Alignment.Center) { AppIcon(a.icon, Icons.Default.Apps, 30.dp) }
        }
        Text(a.name, Modifier.padding(top = 6.dp), style = MaterialTheme.typography.labelMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** A ring showing [frac] (0..1), drawn as an arc; [color] for the value, track in surface variant. */
@Composable
private fun Ring(frac: Float, color: Color, modifier: Modifier, stroke: Dp, label: (@Composable () -> Unit)? = null) {
    Box(modifier, contentAlignment = Alignment.Center) {
        val track = MaterialTheme.colorScheme.surfaceVariant
        Canvas(Modifier.fillMaxSize()) {
            val s = stroke.toPx()
            val inset = s / 2
            val arc = Size(size.width - s, size.height - s)
            drawArc(track, 0f, 360f, false, Offset(inset, inset), arc, style = Stroke(s, cap = StrokeCap.Round))
            drawArc(color, -90f, 360f * frac.coerceIn(0f, 1f), false, Offset(inset, inset), arc, style = Stroke(s, cap = StrokeCap.Round))
        }
        label?.invoke()
    }
}

private fun levelTone(level: String): Tone = when (level) {
    "error", "danger", "critical" -> Tone.DANGER
    "warning", "warn" -> Tone.WARN
    else -> Tone.ACCENT
}

@Composable
private fun StorageCard(ov: Overview, onOpen: () -> Unit) {
    val total = ov.volumes.sumOf { it.total }
    val used = ov.volumes.sumOf { it.used }
    val frac = if (total > 0) used.toFloat() / total else 0f
    SectionCard(stringResource(R.string.status_storage), Modifier.padding(horizontal = 16.dp, vertical = 6.dp), onClick = onOpen, trailing = {
        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Ring(frac, if (frac > 0.9f) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary, Modifier.size(76.dp), 9.dp) {
                Text("${(frac * 100).toInt()}%", style = MaterialTheme.typography.titleMedium)
            }
            Column(Modifier.padding(start = 18.dp)) {
                Text(formatBytes(used) + " / " + formatBytes(total), style = MaterialTheme.typography.titleMedium)
                Text(stringResource(R.string.a_disks_n, ov.disks), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        ov.volumes.forEach { v -> VolumeLine(v) }
    }
}

/** A small line chart of [values] (0..1), filled under the line. */
@Composable
private fun Spark(values: List<Float>, color: Color, modifier: Modifier) {
    Canvas(modifier) {
        if (values.size < 2) return@Canvas
        val step = size.width / (values.size - 1)
        fun y(v: Float) = size.height - v.coerceIn(0f, 1f) * size.height * 0.92f - size.height * 0.04f
        val path = Path()
        values.forEachIndexed { i, v -> if (i == 0) path.moveTo(0f, y(v)) else path.lineTo(i * step, y(v)) }
        val fill = Path().apply { addPath(path); lineTo(size.width, size.height); lineTo(0f, size.height); close() }
        drawPath(fill, Brush.verticalGradient(listOf(color.copy(alpha = 0.3f), color.copy(alpha = 0.02f))))
        drawPath(path, color, style = Stroke(2.dp.toPx(), cap = StrokeCap.Round))
    }
}

@Composable
private fun ActivityCard(stats: StatsHistory) {
    val h = stats.history
    val cpu = h.map { it.cpu.toFloat() / 100f }
    val mem = h.map { s -> if (s.memTotal > 0) s.memUsed.toFloat() / s.memTotal else 0f }
    val last = h.last()
    SectionCard(stringResource(R.string.home_activity), Modifier.padding(horizontal = 16.dp, vertical = 6.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.home_cpu) + " · " + last.cpu.toInt() + "%", style = MaterialTheme.typography.labelMedium)
                Spark(cpu, MaterialTheme.colorScheme.primary, Modifier.fillMaxWidth().height(56.dp).padding(top = 4.dp))
            }
            Column(Modifier.weight(1f)) {
                Text(stringResource(R.string.home_ram) + " · " + formatBytes(last.memUsed), style = MaterialTheme.typography.labelMedium)
                Spark(mem, Tiles.storage.to, Modifier.fillMaxWidth().height(56.dp).padding(top = 4.dp))
            }
        }
        Text(
            stringResource(R.string.home_throughput, formatBytes(last.netRx.toLong()) + "/s", formatBytes(last.netTx.toLong()) + "/s", formatBytes(last.diskRead.toLong()) + "/s", formatBytes(last.diskWrite.toLong()) + "/s"),
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

@Composable
private fun NotificationsCard(events: List<NasEvent>, onOpen: () -> Unit) {
    SectionCard(stringResource(R.string.status_notifications), Modifier.padding(horizontal = 16.dp, vertical = 6.dp), onClick = onOpen, trailing = {
        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }) {
        events.asReversed().take(5).forEach { e ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                val tone = levelTone(e.level)
                Box(Modifier.size(32.dp).background(tone.color().copy(alpha = 0.13f), CircleShape), contentAlignment = Alignment.Center) {
                    Icon(if (tone == Tone.DANGER) Icons.Default.Error else if (tone == Tone.WARN) Icons.Default.Warning else Icons.Default.Notifications, null, Modifier.size(18.dp), tint = tone.color())
                }
                Column(Modifier.padding(start = 12.dp)) {
                    Text(e.subject, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(formatDate(e.time), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
    }
}

/** Details of the NAS, from the header's name. */
@Composable
private fun NasSheet(d: HomeData?, onStatus: () -> Unit, onSettings: () -> Unit) {
    val cfg = Graph.prefs.server
    Column(Modifier.fillMaxWidth().padding(24.dp, 0.dp, 24.dp, 32.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        NasIllustration(Modifier.size(150.dp, 124.dp), healthy = d?.overview?.alerts.isNullOrEmpty())
        Text(d?.info?.hostname ?: cfg?.hostname.orEmpty(), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.padding(top = 8.dp))
        Text(cfg?.url.orEmpty(), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
        d?.info?.let { Text(stringResource(R.string.status_version, it.version, formatUptime(it.uptime)), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        d?.info?.let { NasNewerBanner(it.version) }
        Text(stringResource(R.string.signed_in_as, cfg?.fullName?.ifEmpty { null } ?: cfg?.username.orEmpty(), cfg?.deviceName.orEmpty()), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Row(Modifier.padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            OutlinedButton(onClick = onSettings) { Text(stringResource(R.string.a_app_settings)) }
            Button(onClick = onStatus) { Text(stringResource(R.string.tab_status)) }
        }
    }
}

/** Uploads, photo backup and folder sync: what runs now, with a way to each. */
@Composable
private fun TransfersSheet(t: Transfers, backupOn: Boolean, onBackup: () -> Unit, onSync: () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(bottom = 24.dp)) {
        Text(stringResource(R.string.ui_transfers), style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(24.dp, 0.dp, 24.dp, 12.dp))
        NavRow(Icons.Default.Upload, Tiles.apps, stringResource(R.string.ui_uploads),
            if (t.uploads > 0) pluralStringResource(R.plurals.ui_uploads_running, t.uploads, t.uploads) else stringResource(R.string.ui_no_uploads),
            trailing = { if (t.uploads > 0) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp) }) {}
        NavRow(Icons.Default.Backup, Tiles.backup, stringResource(R.string.channel_backup),
            stringResource(if (t.backup) R.string.ui_running else if (backupOn) R.string.ui_backup_card_on else R.string.ui_backup_card_off), onClick = onBackup)
        NavRow(Icons.Default.Sync, Tiles.sync, stringResource(R.string.sync_title), if (t.sync) stringResource(R.string.ui_running) else null, onClick = onSync)
    }
}
