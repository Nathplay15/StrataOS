package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.height
import androidx.compose.ui.draw.clip
import androidx.compose.material3.IconButton
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.MonitorHeart
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.VerticalDivider
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R

/** The areas a user sees: administrators all of them, the others those not reserved to administrators. */
fun visibleSections(all: List<ManageSection>, admin: Boolean): List<ManageSection> = all.filter { admin || !it.adminOnly }.distinctBy { it.id }

/** The id of the status page among the areas (it is not one of them). */
const val STATUS = "status"

/** Opens a folder of the NAS in the Files tab (from a management area: snapshots, shares…). */
val LocalOpenInFiles = androidx.compose.runtime.staticCompositionLocalOf<(String) -> Unit> { {} }

/** Opens a page of the web interface (path, title) inside the app: rare expert pages. */
val LocalOpenWeb = androidx.compose.runtime.staticCompositionLocalOf<(String, String?) -> Unit> { { _, _ -> } }

/** Opens a full screen page of the app (settings, app update…). */
val LocalOpenOverlay = androidx.compose.runtime.staticCompositionLocalOf<(String?) -> Unit> { {} }

/**
 * The Manage tab: the NAS status and the management areas. Phones show a
 * grid and open an area full screen; tablets keep the list beside the area.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ManageScreen(accountAction: @Composable () -> Unit, initialSection: String? = null, closeTo: (() -> Unit)? = null, onBack: (() -> Unit)? = null) {
    var web by rememberSaveable { mutableStateOf<String?>(null) }
    var webTitle by rememberSaveable { mutableStateOf<String?>(null) }
    web?.let { path ->
        io.github.nathplay15.strataos.ui.web.WebArea({ web = null }, path, webTitle)
        return
    }
    androidx.compose.runtime.CompositionLocalProvider(LocalOpenWeb provides { p, t -> webTitle = t; web = p }) {
        ManageContent(accountAction, initialSection, closeTo, onBack)
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ManageContent(accountAction: @Composable () -> Unit, initialSection: String?, closeTo: (() -> Unit)?, onBack: (() -> Unit)?) {
    val admin = Graph.prefs.server?.isAdmin == true
    val sections = visibleSections(manageSectionsA + manageSectionsB, admin)
    var open by rememberSaveable { mutableStateOf(if (initialSection == STATUS) STATUS else sections.firstOrNull { it.id == initialSection }?.id) }
    // An area opened from elsewhere (the Home grid) goes back there when closed.
    val close: () -> Unit = { if (closeTo != null && open == initialSection) closeTo() else open = null }
    val wide = LocalConfiguration.current.screenWidthDp >= WIDE_DP
    val current = sections.firstOrNull { it.id == open }

    if (wide) {
        Row(Modifier.fillMaxSize()) {
            Column(Modifier.width(300.dp).fillMaxHeight()) {
                BrandTopBar(stringResource(R.string.a_manage), actions = { accountAction() })
                LazyColumn {
                    item { SectionRow(stringResource(R.string.tab_status), Icons.Default.MonitorHeart, open == null || open == STATUS) { open = null } }
                    items(sections, key = { it.id }) { s -> SectionRow(stringResource(s.label), s.icon, open == s.id) { open = s.id } }
                }
            }
            VerticalDivider()
            Box(Modifier.weight(1f).fillMaxHeight()) {
                if (current != null) current.content { open = null } else StatusScreen()
            }
        }
        return
    }

    if (open == STATUS) {
        BackTo(onBack = close)
        Column(Modifier.fillMaxSize()) { StatusScreen(onBack = close) }
        return
    }
    if (current != null) {
        BackTo(onBack = close)
        current.content(close)
        return
    }
    Column(Modifier.fillMaxSize()) {
        BrandTopBar(stringResource(R.string.a_manage), onBack, actions = { accountAction() })
        val status = rememberLoad(Unit) { val a = api(); a.systemInfo() to a.overview() }
        val ctx = androidx.compose.ui.platform.LocalContext.current
        androidx.compose.material3.pulltorefresh.PullToRefreshBox(isRefreshing = status.refreshing, onRefresh = status.reload) {
            LazyVerticalGrid(GridCells.Adaptive(150.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(12.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    // The NAS on the brand gradient: white text, light bar.
                    androidx.compose.runtime.CompositionLocalProvider(androidx.compose.material3.LocalContentColor provides androidx.compose.ui.graphics.Color.White) {
                    Box(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).background(strata.header).clickable { open = STATUS }) {
                        Column(Modifier.padding(18.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                            val st = status.state
                            if (st is Loaded.Failed) Text(errorText(ctx, st.error))
                            val (info, ov) = status.value ?: (null to null)
                            if (info == null || ov == null) {
                                if (st is Loaded.Loading) androidx.compose.material3.LinearProgressIndicator(Modifier.fillMaxWidth())
                                Text(stringResource(R.string.a_open_status), style = MaterialTheme.typography.labelLarge)
                                return@Column
                            }
                            Text(info.hostname, style = MaterialTheme.typography.headlineSmall)
                            Text(stringResource(R.string.a_version, info.version), style = MaterialTheme.typography.bodySmall)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Icon(if (ov.alerts.isEmpty()) Icons.Default.CheckCircle else Icons.Default.Warning, null)
                                Text(if (ov.alerts.isEmpty()) stringResource(R.string.status_all_good) else stringResource(R.string.a_alerts_n, ov.alerts.size), Modifier.padding(start = 8.dp))
                            }
                            val total = ov.volumes.sumOf { it.total }
                            if (total > 0) {
                                val used = ov.volumes.sumOf { it.used }
                                androidx.compose.material3.LinearProgressIndicator(
                                    progress = { (used.toFloat() / total).coerceIn(0f, 1f) }, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp).height(6.dp),
                                    color = androidx.compose.ui.graphics.Color.White, trackColor = androidx.compose.ui.graphics.Color.White.copy(alpha = 0.3f),
                                    strokeCap = androidx.compose.ui.graphics.StrokeCap.Round, gapSize = 0.dp, drawStopIndicator = {},
                                )
                                Text(stringResource(R.string.a_usage, formatBytes(used), formatBytes(total), (used * 100 / total).toInt()), style = MaterialTheme.typography.bodySmall)
                            }
                            Text(stringResource(R.string.a_open_status), style = MaterialTheme.typography.labelLarge)
                        }
                        if (status.value != null) NasIllustration(Modifier.align(Alignment.TopEnd).padding(16.dp).size(76.dp, 62.dp), healthy = status.value?.second?.alerts.isNullOrEmpty())
                    }
                    }
                }
                items(sections, key = { it.id }) { s ->
                    Card(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.medium).clickable { open = s.id }) {
                        Column(Modifier.padding(16.dp)) {
                            TileIcon(s.icon, tileOf(s.id), size = 44.dp, iconSize = 24.dp)
                            Text(stringResource(s.label), Modifier.padding(top = 12.dp), style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun SectionRow(label: String, icon: androidx.compose.ui.graphics.vector.ImageVector, selected: Boolean, onClick: () -> Unit) {
    ListItem(
        headlineContent = { Text(label) },
        leadingContent = { Icon(icon, null) },
        colors = if (selected) ListItemDefaults.colors(containerColor = MaterialTheme.colorScheme.secondaryContainer) else ListItemDefaults.colors(),
        modifier = Modifier.clickable(onClick = onClick),
    )
}

/** The tile colours of a management area. */
fun tileOf(id: String): TilePaint = when (id) {
    "storage" -> Tiles.storage
    "apps" -> Tiles.apps
    "links" -> Tiles.links
    "surveillance" -> Tiles.camera
    "vms" -> Tiles.vms
    "vpn" -> Tiles.vpn
    "users" -> Tiles.users
    "fleet" -> Tiles.fleet
    "nas-settings" -> Tiles.settings
    "web" -> Tiles.web
    "backups" -> Tiles.backup
    "notifications" -> Tiles.camera
    "sessions" -> Tiles.status
    "cinema" -> Tiles.photos
    "music" -> Tiles.assistant
    else -> Tiles.more
}
