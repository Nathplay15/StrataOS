package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.clickable
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
import androidx.compose.material3.TopAppBar
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

private const val STATUS = "status"

/** Opens a folder of the NAS in the Files tab (from a management area: snapshots, shares…). */
val LocalOpenInFiles = androidx.compose.runtime.staticCompositionLocalOf<(String) -> Unit> { {} }

/**
 * The Manage tab: the NAS status and the management areas. Phones show a
 * grid and open an area full screen; tablets keep the list beside the area.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ManageScreen(accountAction: @Composable () -> Unit) {
    val admin = Graph.prefs.server?.isAdmin == true
    val sections = visibleSections(manageSectionsA + manageSectionsB, admin)
    var open by rememberSaveable { mutableStateOf<String?>(null) }
    val wide = LocalConfiguration.current.screenWidthDp >= WIDE_DP
    val current = sections.firstOrNull { it.id == open }

    if (wide) {
        Row(Modifier.fillMaxSize()) {
            Column(Modifier.width(300.dp).fillMaxHeight()) {
                TopAppBar(title = { Text(stringResource(R.string.a_manage)) }, actions = { accountAction() })
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
        BackTo { open = null }
        Column(Modifier.fillMaxSize()) { StatusScreen() }
        return
    }
    if (current != null) {
        BackTo { open = null }
        current.content { open = null }
        return
    }
    Column(Modifier.fillMaxSize()) {
        TopAppBar(title = { Text(stringResource(R.string.a_manage)) }, actions = { accountAction() })
        val status = rememberLoad(Unit) { val a = api(); a.systemInfo() to a.overview() }
        Refreshable(status) { (info, ov) ->
            LazyVerticalGrid(GridCells.Adaptive(150.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(12.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    Card(Modifier.fillMaxWidth().clickable { open = STATUS }) {
                        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                            Text(info.hostname, style = MaterialTheme.typography.titleLarge)
                            Text(stringResource(R.string.a_version, info.version), style = MaterialTheme.typography.bodySmall)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                if (ov.alerts.isEmpty()) Icon(Icons.Default.CheckCircle, null, tint = Tone.OK.color())
                                else Icon(Icons.Default.Warning, null, tint = Tone.WARN.color())
                                Text(if (ov.alerts.isEmpty()) stringResource(R.string.status_all_good) else stringResource(R.string.a_alerts_n, ov.alerts.size), Modifier.padding(start = 8.dp))
                            }
                            val total = ov.volumes.sumOf { it.total }
                            if (total > 0) UsageBar(ov.volumes.sumOf { it.used }, total)
                            Text(stringResource(R.string.a_open_status), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.primary)
                        }
                    }
                }
                items(sections, key = { it.id }) { s ->
                    Card(Modifier.fillMaxWidth().clickable { open = s.id }) {
                        Column(Modifier.padding(16.dp)) {
                            Icon(s.icon, null, Modifier.size(32.dp), tint = MaterialTheme.colorScheme.primary)
                            Text(stringResource(s.label), Modifier.padding(top = 10.dp), style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
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
