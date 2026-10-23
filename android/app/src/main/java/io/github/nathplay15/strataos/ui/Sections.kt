package io.github.nathplay15.strataos.ui

import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.vector.ImageVector

/**
 * A management area of the NAS (storage, apps, surveillance…). Each area lives
 * in its own package (ui/<area>/) and is listed in SectionsA.kt or SectionsB.kt.
 * [adminOnly] areas are hidden for the other users (the NAS refuses them anyway).
 */
data class ManageSection(
    val id: String,
    @StringRes val label: Int,
    val icon: ImageVector,
    val adminOnly: Boolean,
    val content: @Composable (back: () -> Unit) -> Unit,
)
