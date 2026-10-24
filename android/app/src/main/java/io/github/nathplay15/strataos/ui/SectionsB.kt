package io.github.nathplay15.strataos.ui

import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.Videocam
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.surveillance.SurveillanceArea
import io.github.nathplay15.strataos.ui.vms.VmsArea

/** Management areas, in display order (see ManageSection). */
val manageSectionsB: List<ManageSection> = listOf(
    ManageSection("surveillance", R.string.area_surveillance, Icons.Default.Videocam, adminOnly = true) { SurveillanceArea(it) },
    ManageSection("vms", R.string.area_vms, Icons.Default.Computer, adminOnly = true) { VmsArea(it) },
)
