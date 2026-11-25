package io.github.nathplay15.strataos.ui

import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Apps
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Storage
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.ui.apps.AppsSection
import io.github.nathplay15.strataos.ui.files.ShareLinksScreen
import io.github.nathplay15.strataos.ui.storage.StorageSection

/** Management areas, in display order (see ManageSection). */
val manageSectionsA: List<ManageSection> = listOf(
    ManageSection("storage", R.string.a_storage, Icons.Default.Storage, adminOnly = true) { back -> StorageSection(back) },
    ManageSection("backups", R.string.ctl_backups, Icons.Default.Backup, adminOnly = true) { back -> io.github.nathplay15.strataos.ui.control.BackupsScreen(back) },
    ManageSection("apps", R.string.a_applications, Icons.Default.Apps, adminOnly = true) { back -> AppsSection(back) },
    ManageSection("notifications", R.string.notifications, Icons.Default.Notifications, adminOnly = true) { back -> io.github.nathplay15.strataos.ui.nassettings.NasSettingsArea(back, "inbox") },
    ManageSection("links", R.string.a_share_links, Icons.Default.Link, adminOnly = false) { back -> ShareLinksScreen(back) },
)
