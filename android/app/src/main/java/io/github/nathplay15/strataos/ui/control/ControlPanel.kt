package io.github.nathplay15.strataos.ui.control

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Article
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material.icons.filled.Badge
import androidx.compose.material.icons.filled.Block
import androidx.compose.material.icons.filled.Bolt
import androidx.compose.material.icons.filled.CloudSync
import androidx.compose.material.icons.filled.Coronavirus
import androidx.compose.material.icons.filled.Devices
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.EventRepeat
import androidx.compose.material.icons.filled.GppGood
import androidx.compose.material.icons.filled.Help
import androidx.compose.material.icons.filled.History
import androidx.compose.material.icons.filled.Lan
import androidx.compose.material.icons.filled.Memory
import androidx.compose.material.icons.filled.Movie
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.NotificationsActive
import androidx.compose.material.icons.filled.Policy
import androidx.compose.material.icons.filled.PowerSettingsNew
import androidx.compose.material.icons.filled.Public
import androidx.compose.material.icons.filled.Security
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.SettingsEthernet
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material.icons.filled.SystemUpdate
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material.icons.filled.Upgrade
import androidx.compose.material.icons.filled.VerifiedUser
import androidx.compose.material.icons.filled.Wifi
import androidx.compose.material.icons.filled.EnergySavingsLeaf
import androidx.compose.material.icons.filled.DriveFileMove
import androidx.compose.material.icons.filled.Hub
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.LocalOpenWeb
import io.github.nathplay15.strataos.ui.NavGroup
import io.github.nathplay15.strataos.ui.NavRow
import io.github.nathplay15.strataos.ui.TilePaint
import io.github.nathplay15.strataos.ui.Tiles
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.readable

/** A row of the Control panel: a native screen ([route]) or a page of the web interface ([web]). */
class CtlRow(val icon: ImageVector, @StringRes val label: Int, val paint: TilePaint, val route: String? = null, val web: String? = null)

class CtlGroup(@StringRes val title: Int, val rows: List<CtlRow>)

/** Everything of the web Control Panel and system apps, grouped like the Me tab. */
val controlGroups = listOf(
    CtlGroup(R.string.ctl_g_alerts, listOf(
        CtlRow(Icons.Default.Notifications, R.string.notifications, Tiles.camera, "inbox"),
        CtlRow(Icons.Default.NotificationsActive, R.string.nas_notifications, Tiles.users, "notifications"),
        CtlRow(Icons.Default.SystemUpdate, R.string.nas_updates, Tiles.apps, "updates"),
        CtlRow(Icons.Default.Upgrade, R.string.ctl_dist, Tiles.fleet, "dist"),
    )),
    CtlGroup(R.string.ctl_g_system, listOf(
        CtlRow(Icons.Default.PowerSettingsNew, R.string.nas_power, Tiles.camera, "power"),
        CtlRow(Icons.Default.EnergySavingsLeaf, R.string.ctl_energy, Tiles.backup, "energy"),
        CtlRow(Icons.Default.Memory, R.string.ctl_hardware, Tiles.vms, "hardware"),
        CtlRow(Icons.Default.Badge, R.string.ctl_identity, Tiles.status, "identity"),
        CtlRow(Icons.Default.Dns, R.string.nas_services, Tiles.storage, "services"),
        CtlRow(Icons.Default.History, R.string.nas_restore, Tiles.sync, "restore"),
        CtlRow(Icons.AutoMirrored.Filled.Article, R.string.ctl_logs, Tiles.assistant, "logs"),
        CtlRow(Icons.Default.EventRepeat, R.string.ctl_schedules, Tiles.links, "schedules"),
        CtlRow(Icons.Default.Bolt, R.string.ctl_routines, Tiles.files, "routines"),
    )),
    CtlGroup(R.string.ctl_g_security, listOf(
        CtlRow(Icons.Default.GppGood, R.string.ctl_advisor, Tiles.backup, "advisor"),
        CtlRow(Icons.Default.Block, R.string.ctl_guard, Tiles.camera, "guard"),
        CtlRow(Icons.Default.Devices, R.string.ctl_sessions, Tiles.apps, "sessions"),
        CtlRow(Icons.Default.Policy, R.string.ctl_logins, Tiles.fleet, "logins"),
        CtlRow(Icons.Default.Coronavirus, R.string.ctl_antivirus, Tiles.vpn, "antivirus"),
        CtlRow(Icons.Default.AccountCircle, R.string.nas_security, Tiles.users, "security"),
    )),
    CtlGroup(R.string.ctl_g_network, listOf(
        CtlRow(Icons.Default.Lan, R.string.nas_network, Tiles.storage, "network"),
        CtlRow(Icons.Default.VerifiedUser, R.string.ctl_certs, Tiles.backup, "certs"),
        CtlRow(Icons.Default.Public, R.string.ctl_w_remote, Tiles.web, web = "/system/remote"),
        CtlRow(Icons.Default.Wifi, R.string.ctl_w_wifi, Tiles.sync, web = "/system/network#wifi"),
        CtlRow(Icons.Default.Shield, R.string.ctl_w_firewall, Tiles.camera, web = "/system/network#firewall"),
    )),
    CtlGroup(R.string.ctl_g_web, listOf(
        CtlRow(Icons.Default.Storage, R.string.ctl_w_storage, Tiles.storage, web = "/storage/lvm"),
        CtlRow(Icons.Default.DriveFileMove, R.string.ctl_w_migration, Tiles.status, web = "/migration"),
        CtlRow(Icons.Default.CloudSync, R.string.ctl_w_cloudsync, Tiles.sync, web = "/cloudsync"),
        CtlRow(Icons.Default.Security, R.string.ctl_w_sso, Tiles.users, web = "/system/sso"),
        CtlRow(Icons.Default.SettingsEthernet, R.string.ctl_w_directory, Tiles.fleet, web = "/system/directory"),
        CtlRow(Icons.Default.Hub, R.string.ctl_w_ha, Tiles.vms, web = "/system/ha"),
        CtlRow(Icons.Default.Settings, R.string.ctl_w_settings, Tiles.settings, web = "/system/settings"),
        CtlRow(Icons.Default.Terminal, R.string.ctl_w_terminal, Tiles.vms, web = "/system/terminal"),
        CtlRow(Icons.Default.Movie, R.string.ctl_w_media, Tiles.photos, web = "/media"),
        CtlRow(Icons.Default.Help, R.string.ctl_w_help, Tiles.backup, web = "/help"),
    )),
)

/** The menu of the Control panel; [open] shows a native screen by its route. */
@Composable
fun ControlMenu(back: () -> Unit, open: (String) -> Unit) {
    val web = LocalOpenWeb.current
    AreaScreen(stringResource(R.string.area_nas_settings), back) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).readable().padding(16.dp, 4.dp, 16.dp, 24.dp), verticalArrangement = Arrangement.spacedBy(20.dp)) {
            controlGroups.forEach { g ->
                NavGroup(stringResource(g.title), g.rows.map { row ->
                    @Composable {
                        val label = stringResource(row.label)
                        if (row.web != null) NavRow(row.icon, row.paint, label, stringResource(R.string.ctl_in_web), trailing = {
                            Icon(Icons.AutoMirrored.Filled.OpenInNew, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                        }) { web(row.web, label) }
                        else NavRow(row.icon, row.paint, label) { open(row.route!!) }
                    }
                })
            }
            Hint(stringResource(R.string.ctl_account_hint))
        }
    }
}

/** The native screens of the Control panel added in 0.13.0 (the older ones live in ui/nassettings). */
@Composable
fun ControlScreen(route: String, back: () -> Unit): Boolean {
    when (route) {
        "inbox" -> InboxScreen(back)
        "dist" -> DistUpgradeScreen(back)
        "energy" -> EnergyScreen(back)
        "hardware" -> HardwareScreen(back)
        "identity" -> IdentityScreen(back)
        "logs" -> LogsScreen(back)
        "schedules" -> SchedulesScreen(back)
        "routines" -> RoutinesScreen(back)
        "advisor" -> AdvisorScreen(back)
        "guard" -> GuardScreen(back)
        "sessions" -> SessionsScreen(back)
        "logins" -> LoginsScreen(back)
        "antivirus" -> AntivirusScreen(back)
        "certs" -> CertificatesScreen(back)
        else -> return false
    }
    return true
}
