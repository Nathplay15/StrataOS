package io.github.nathplay15.strataos.ui.files

import androidx.compose.foundation.clickable
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.DownloadDone
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.PhotoAlbum
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.filled.StarBorder
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.data.Favorite
import io.github.nathplay15.strataos.data.OfflineSyncWorker
import io.github.nathplay15.strataos.data.favorites
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.net.ShareLink
import io.github.nathplay15.strataos.net.deleteShareLink
import io.github.nathplay15.strataos.net.linkUrl
import io.github.nathplay15.strataos.net.shareLinks
import io.github.nathplay15.strataos.ui.BackTo
import io.github.nathplay15.strataos.ui.Chip
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.RefreshAction
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.SectionScaffold
import io.github.nathplay15.strataos.ui.Tone
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate
import io.github.nathplay15.strataos.ui.iconOf
import io.github.nathplay15.strataos.ui.openWith
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import io.github.nathplay15.strataos.ui.toast

/** The share links of the user (all of them for administrators): copy, share, delete. */
@Composable
fun ShareLinksScreen(back: (() -> Unit)?) {
    val ctx = LocalContext.current
    val load = rememberLoad(Unit) { api().shareLinks() }
    val act = rememberActions()
    var deleting by remember { mutableStateOf<ShareLink?>(null) }
    if (back != null) BackTo(onBack = back)
    SectionScaffold(stringResource(R.string.a_share_links), back, actions = { RefreshAction(load.reload) }) { m ->
        Refreshable(load, m) { links ->
            if (links.isEmpty()) EmptyState(Icons.Default.Link, stringResource(R.string.a_no_links))
            else LazyColumn {
                items(links, key = { it.id }) { l ->
                    val u = api().linkUrl(l.id)
                    ListItem(
                        headlineContent = { Text(l.name.ifEmpty { l.path }, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                        supportingContent = {
                            Text(buildList {
                                add(if (l.album.isNotEmpty()) stringResource(R.string.a_album) else displayPath(l.path.substringBeforeLast('/')))
                                if (l.expires.isNotEmpty()) add(stringResource(R.string.a_link_expires, formatDate(l.expires)))
                                add(stringResource(R.string.a_link_counts, l.downloads, l.uploads))
                            }.joinToString(" · "), maxLines = 2)
                        },
                        leadingContent = {
                            Icon(when { l.album.isNotEmpty() -> Icons.Default.PhotoAlbum; l.password -> Icons.Default.Lock; else -> Icons.Default.Link }, null)
                        },
                        trailingContent = {
                            androidx.compose.foundation.layout.Row {
                                IconButton(onClick = { copyText(ctx, l.name, u); toast(ctx, ctx.getString(R.string.a_copied)) }) { Icon(Icons.Default.ContentCopy, stringResource(R.string.a_copy_link)) }
                                IconButton(onClick = { shareText(ctx, u) }) { Icon(Icons.Default.Share, stringResource(R.string.share)) }
                                IconButton(onClick = { deleting = l }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete)) }
                            }
                        },
                    )
                    HorizontalDivider()
                }
            }
        }
    }
    deleting?.let { l ->
        ConfirmDialog(stringResource(R.string.a_delete_link), stringResource(R.string.a_delete_link_text, l.name), stringResource(R.string.a_delete),
            onConfirm = { act.run(load.reload) { api().deleteShareLink(l.id) } }, onDismiss = { deleting = null }, danger = true)
    }
}

/** Favorites of this phone: folders open in the browser, offline files open without the NAS. */
@Composable
fun FavoritesScreen(back: () -> Unit, openFolder: (String) -> Unit, open: (FileEntry) -> Unit) {
    val ctx = LocalContext.current
    var tick by remember { mutableIntStateOf(0) }
    val list = remember(tick) { favorites.all().sortedWith(compareBy<Favorite> { !it.dir }.thenBy { it.name.lowercase() }) }
    BackTo(onBack = back)
    SectionScaffold(stringResource(R.string.a_favorites), back, actions = {
        if (list.any { it.offline }) IconButton(onClick = { OfflineSyncWorker.syncNow(ctx); toast(ctx, ctx.getString(R.string.a_sync_started)) }) {
            Icon(Icons.Default.Sync, stringResource(R.string.a_sync_now))
        }
    }) { m ->
        if (list.isEmpty()) EmptyState(Icons.Default.StarBorder, stringResource(R.string.a_no_favorites), m)
        else LazyColumn(m) {
            items(list, key = { it.path }) { f ->
                val e = FileEntry(f.name, f.path, f.dir, f.size, f.modTime)
                ListItem(
                    headlineContent = { Text(f.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    supportingContent = {
                        Text(buildList {
                            add(displayPath(f.path.substringBeforeLast('/')))
                            if (f.offline) add(if (f.size > 0) formatBytes(f.size) else stringResource(R.string.a_offline_pending))
                            if (f.synced > 0) add(stringResource(R.string.a_synced, io.github.nathplay15.strataos.ui.formatTime(f.synced)))
                        }.joinToString(" · "), maxLines = 2)
                    },
                    leadingContent = { Icon(iconOf(e), null) },
                    trailingContent = {
                        androidx.compose.foundation.layout.Row {
                            if (f.missing) Chip(stringResource(R.string.a_missing), Tone.DANGER)
                            else if (f.offline) Icon(Icons.Default.DownloadDone, stringResource(R.string.a_offline_kept), tint = MaterialTheme.colorScheme.primary)
                            IconButton(onClick = { favorites.remove(f.path); tick++ }) { Icon(Icons.Default.Star, stringResource(R.string.a_unfavorite), tint = MaterialTheme.colorScheme.primary) }
                        }
                    },
                    modifier = Modifier.clickable {
                        val copy = favorites.usableCopy(f.path)
                        when {
                            f.dir -> openFolder(f.path)
                            copy != null && f.missing -> openWith(ctx, copy)
                            else -> open(e)
                        }
                    },
                )
                HorizontalDivider()
            }
        }
    }
}
