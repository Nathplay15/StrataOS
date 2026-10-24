package io.github.nathplay15.strataos.ui

import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Videocam
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.surveillance.SurveillanceArea

/** Management areas, in display order (see ManageSection). */
val manageSectionsB: List<ManageSection> = listOf(
    ManageSection("surveillance", R.string.area_surveillance, Icons.Default.Videocam, adminOnly = true) { SurveillanceArea(it) },
)
