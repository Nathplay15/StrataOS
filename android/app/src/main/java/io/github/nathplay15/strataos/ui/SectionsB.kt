package io.github.nathplay15.strataos.ui

import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.Group
import androidx.compose.material.icons.filled.Hub
import androidx.compose.material.icons.filled.Language
import androidx.compose.material.icons.filled.Tune
import androidx.compose.material.icons.filled.Videocam
import androidx.compose.material.icons.filled.VpnKey
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.fleet.FleetArea
import io.github.nathplay15.strataos.ui.nassettings.NasSettingsArea
import io.github.nathplay15.strataos.ui.surveillance.SurveillanceArea
import io.github.nathplay15.strataos.ui.users.UsersArea
import io.github.nathplay15.strataos.ui.vms.VmsArea
import io.github.nathplay15.strataos.ui.vpn.VpnArea
import io.github.nathplay15.strataos.ui.web.WebArea

/** Management areas, in display order (see ManageSection). */
val manageSectionsB: List<ManageSection> = listOf(
    ManageSection("surveillance", R.string.area_surveillance, Icons.Default.Videocam, adminOnly = true) { SurveillanceArea(it) },
    ManageSection("vms", R.string.area_vms, Icons.Default.Computer, adminOnly = true) { VmsArea(it) },
    // Users who are not administrators see their own VPN devices there.
    ManageSection("vpn", R.string.area_vpn, Icons.Default.VpnKey, adminOnly = false) { VpnArea(it) },
    ManageSection("users", R.string.area_users, Icons.Default.Group, adminOnly = true) { UsersArea(it) },
    ManageSection("fleet", R.string.area_fleet, Icons.Default.Hub, adminOnly = true) { FleetArea(it) },
    ManageSection("nas-settings", R.string.area_nas_settings, Icons.Default.Tune, adminOnly = true) { NasSettingsArea(it) },
    // The whole web interface, with the rights of the signed-in user.
    ManageSection("web", R.string.area_web, Icons.Default.Language, adminOnly = false) { WebArea(it) },
)
