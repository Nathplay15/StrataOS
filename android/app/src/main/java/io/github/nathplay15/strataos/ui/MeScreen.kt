package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Logout
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.Dashboard
import androidx.compose.material.icons.filled.Language
import androidx.compose.material.icons.filled.Devices
import androidx.compose.material.icons.filled.Tune
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.MonitorHeart
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.SystemUpdate
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.BuildConfig
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.backup.BackupSettings
import kotlinx.coroutines.launch

/** The Me tab: who is signed in on which NAS, the backup and sharing of this phone, the app's own settings. */
@Composable
fun MeScreen(go: HomeNav, onSignedOut: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val cfg = Graph.prefs.server
    val admin = cfg?.isAdmin == true
    val name = cfg?.fullName?.ifEmpty { null } ?: cfg?.username.orEmpty()
    val backupOn = remember { BackupSettings(ctx).enabled }
    var confirmSignOut by remember { mutableStateOf(false) }
    val scroll = rememberScrollState()
    Box(Modifier.fillMaxSize()) {
    Column(Modifier.fillMaxSize().verticalScroll(scroll)) {
        Box(Modifier.fillMaxWidth().background(strata.header)) {
            Canvas(Modifier.matchParentSize().clipToBounds()) {
                drawCircle(Color.White.copy(alpha = 0.09f), radius = size.width * 0.4f, center = Offset(size.width * 0.92f, size.height * 0.1f))
            }
            Row(Modifier.windowInsetsPadding(WindowInsets.statusBars).padding(20.dp, 28.dp, 20.dp, 54.dp), verticalAlignment = Alignment.CenterVertically) {
                // The initials of the user on a light disc.
                Box(Modifier.size(68.dp).background(Color.White.copy(alpha = 0.22f), CircleShape), contentAlignment = Alignment.Center) {
                    Text(initials(name), style = MaterialTheme.typography.headlineSmall, color = Color.White)
                }
                Column(Modifier.weight(1f).padding(start = 16.dp)) {
                    Text(name, style = MaterialTheme.typography.headlineSmall, color = Color.White, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(
                        stringResource(if (admin) R.string.ui_role_admin else R.string.ui_role_user) + " · " + (cfg?.hostname ?: ""),
                        style = MaterialTheme.typography.bodyMedium, color = Color.White.copy(alpha = 0.85f), maxLines = 1, overflow = TextOverflow.Ellipsis,
                    )
                    Text(cfg?.deviceName.orEmpty(), style = MaterialTheme.typography.bodySmall, color = Color.White.copy(alpha = 0.7f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
            Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(26.dp).background(MaterialTheme.colorScheme.background, RoundedCornerShape(topStart = 26.dp, topEnd = 26.dp)))
        }
        Column(Modifier.padding(16.dp, 0.dp, 16.dp, 24.dp), verticalArrangement = Arrangement.spacedBy(20.dp)) {
            NavGroup(stringResource(R.string.ui_me_data), listOf(
                { NavRow(Icons.Default.Backup, Tiles.backup, stringResource(R.string.channel_backup), stringResource(if (backupOn) R.string.ui_backup_card_on else R.string.ui_backup_card_off)) { go.overlay("backup") } },
                { NavRow(Icons.Default.Sync, Tiles.sync, stringResource(R.string.sync_title)) { go.overlay("sync") } },
                { NavRow(Icons.Default.Link, Tiles.links, stringResource(R.string.a_share_links)) { go.section("links") } },
            ))
            NavGroup(stringResource(R.string.ui_me_nas), buildList {
                add { NavRow(Icons.Default.MonitorHeart, Tiles.status, stringResource(R.string.tab_status), cfg?.url) { go.section(STATUS) } }
                add { NavRow(Icons.Default.Dashboard, Tiles.fleet, stringResource(R.string.a_manage)) { go.tab("manage") } }
                if (admin) add { NavRow(Icons.Default.Tune, Tiles.settings, stringResource(R.string.area_nas_settings)) { go.section("nas-settings") } }
                add { NavRow(Icons.Default.Devices, Tiles.status, stringResource(R.string.ctl_sessions)) { go.section("sessions") } }
                add { NavRow(Icons.Default.Language, Tiles.web, stringResource(R.string.area_web)) { go.section("web") } }
            })
            NavGroup(stringResource(R.string.ui_me_app), listOf(
                { NavRow(Icons.Default.Settings, Tiles.settings, stringResource(R.string.a_app_settings)) { go.overlay("settings") } },
                { NavRow(Icons.Default.SystemUpdate, Tiles.apps, stringResource(R.string.update_title), stringResource(R.string.update_installed, BuildConfig.VERSION_NAME)) { go.overlay("update") } },
            ))
            NavGroup(null, listOf(
                { NavRow(Icons.AutoMirrored.Filled.Logout, Tiles.camera, stringResource(R.string.sign_out), trailing = {}) { confirmSignOut = true } },
            ))
            Text(stringResource(R.string.about, BuildConfig.VERSION_NAME), Modifier.fillMaxWidth(), style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
            Spacer(Modifier.height(8.dp))
        }
    }
    val scrolled by remember { androidx.compose.runtime.derivedStateOf { scroll.value > 300 } }
    StatusBarScrim(scrolled)
    StatusBarIcons(light = !scrolled)
    }
    if (confirmSignOut) AlertDialog(
        onDismissRequest = { confirmSignOut = false },
        title = { Text(stringResource(R.string.sign_out)) },
        text = { Text(stringResource(R.string.sign_out_text)) },
        confirmButton = {
            TextButton(onClick = {
                confirmSignOut = false
                scope.launch { signOut(ctx); onSignedOut() }
            }) { Text(stringResource(R.string.sign_out)) }
        },
        dismissButton = { TextButton(onClick = { confirmSignOut = false }) { Text(stringResource(R.string.cancel)) } },
    )
}

/** One or two letters for the avatar: the first letters of the first two words of the name. */
fun initials(name: String): String =
    name.split(' ', '.', '_', '-').filter { it.isNotBlank() }.take(2).joinToString("") { it.take(1).uppercase() }.ifEmpty { "?" }
