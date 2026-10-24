package io.github.nathplay15.strataos.ui.files

import android.content.Context
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Sort
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.CreateNewFolder
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.DownloadDone
import androidx.compose.material.icons.filled.FolderOff
import androidx.compose.material.icons.filled.GridView
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Share
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.SelectAll
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.filled.Upload
import androidx.compose.material.icons.automirrored.filled.ViewList
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.edit
import androidx.work.WorkInfo
import androidx.work.WorkManager
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.data.Favorite
import io.github.nathplay15.strataos.data.OfflineSyncWorker
import io.github.nathplay15.strataos.data.favorites
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.net.FileList
import io.github.nathplay15.strataos.net.copyFiles
import io.github.nathplay15.strataos.net.imageUrl
import io.github.nathplay15.strataos.net.listFiles
import io.github.nathplay15.strataos.net.photoThumbUrl
import io.github.nathplay15.strataos.net.renameFile
import io.github.nathplay15.strataos.net.searchFiles
import io.github.nathplay15.strataos.net.shares
import io.github.nathplay15.strataos.net.trashFiles
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.FolderPicker
import io.github.nathplay15.strataos.ui.Kind
import io.github.nathplay15.strataos.ui.NasImage
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.TextDialog
import io.github.nathplay15.strataos.ui.Thumbs
import io.github.nathplay15.strataos.ui.Viewer
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.errorText
import io.github.nathplay15.strataos.ui.formatBytes
import io.github.nathplay15.strataos.ui.formatDate
import io.github.nathplay15.strataos.ui.iconOf
import io.github.nathplay15.strataos.ui.kindOf
import io.github.nathplay15.strataos.ui.openWith
import io.github.nathplay15.strataos.ui.photos.ImagePager
import io.github.nathplay15.strataos.ui.photos.PagerImage
import io.github.nathplay15.strataos.ui.player.PlayerActivity
import io.github.nathplay15.strataos.ui.player.QueueItem
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import io.github.nathplay15.strataos.ui.shareFile
import io.github.nathplay15.strataos.ui.stageUris
import io.github.nathplay15.strataos.ui.toast
import io.github.nathplay15.strataos.upload.UploadWorker
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private const val ROOT = "/srv/strata"

/** Display settings of the browser (kept on the phone). */
private class BrowserPrefs(ctx: Context) {
    private val p = ctx.getSharedPreferences("files_ui", Context.MODE_PRIVATE)
    var order: FileOrder
        get() = FileOrder(runCatching { SortBy.valueOf(p.getString("sort", "NAME")!!) }.getOrDefault(SortBy.NAME), p.getBoolean("desc", false))
        set(v) = p.edit { putString("sort", v.by.name); putBoolean("desc", v.descending) }
    var grid: Boolean
        get() = p.getBoolean("grid", false)
        set(v) = p.edit { putBoolean("grid", v) }
}

/** Thumbnail address of a file, null when it has none (the version busts the phone's cache). */
private fun thumbOf(e: FileEntry, px: Int) = when {
    e.dir -> null
    kindOf(e.name) == Kind.IMAGE -> api().imageUrl(e.path, px).newBuilder().addQueryParameter("v", e.modTime).build()
    kindOf(e.name) == Kind.VIDEO -> api().photoThumbUrl(e.path, "s").newBuilder().addQueryParameter("v", e.modTime).build()
    else -> null
}

/** The file browser: folders of the NAS, search, selection, operations, favorites. */
@Composable
fun FilesScreen(path: String, setPath: (String) -> Unit, accountAction: @Composable () -> Unit = {}) {
    var place by rememberSaveable { mutableStateOf("") } // "", "favorites", "links"
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var pdf by remember { mutableStateOf<Pair<FileEntry, File>?>(null) }
    pdf?.let { (e, f) -> Viewer(e, f) { pdf = null }; return }
    when (place) {
        "favorites" -> FavoritesScreen(back = { place = "" }, openFolder = { place = ""; setPath(it) }, open = { e ->
            scope.launch {
                runCatching { withContext(Dispatchers.IO) { localCopy(ctx, e) } }
                    .onSuccess { f -> if (kindOf(e.name) == Kind.PDF) pdf = e to f else openWith(ctx, f) }
                    .onFailure { toast(ctx, errorText(ctx, it)) }
            }
        })
        "links" -> ShareLinksScreen(back = { place = "" })
        else -> Browser(path, setPath, { place = it }, accountAction) { e, f -> pdf = e to f }
    }
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalFoundationApi::class)
@Composable
private fun Browser(path: String, setPath: (String) -> Unit, goPlace: (String) -> Unit, accountAction: @Composable () -> Unit, showPdf: (FileEntry, File) -> Unit) {
    val ctx = LocalContext.current
    val res = androidx.compose.ui.platform.LocalResources.current
    val scope = rememberCoroutineScope()
    val prefs = remember { BrowserPrefs(ctx) }
    var order by remember { mutableStateOf(prefs.order) }
    var grid by remember { mutableStateOf(prefs.grid) }
    val inRecycle = path.contains("/.recycle")
    val load = rememberLoad(path) { api().listFiles(path, hidden = inRecycle || path.contains("/.snapshots")) }
    val shares = rememberLoad(Unit) { runCatching { api().shares() }.getOrDefault(emptyList()) }
    var searching by rememberSaveable { mutableStateOf(false) }
    var query by rememberSaveable { mutableStateOf("") }
    val search = rememberLoad(path, query, searching) {
        if (!searching || query.trim().length < 2) null else { delay(400); api().searchFiles(path, query.trim()) }
    }
    var selected by remember(path) { mutableStateOf(setOf<String>()) }
    var progress by remember { mutableStateOf<Pair<String, Float>?>(null) }
    var menuFor by remember { mutableStateOf<FileEntry?>(null) }
    var sortMenu by remember { mutableStateOf(false) }
    var moreMenu by remember { mutableStateOf(false) }
    var selMenu by remember { mutableStateOf(false) }
    var newFolder by remember { mutableStateOf(false) }
    var renaming by remember { mutableStateOf<FileEntry?>(null) }
    var deleting by remember { mutableStateOf<List<FileEntry>?>(null) }
    var copying by remember { mutableStateOf<Pair<List<FileEntry>, Boolean>?>(null) } // items, move
    var linkFor by remember { mutableStateOf<FileEntry?>(null) }
    var propsFor by remember { mutableStateOf<FileEntry?>(null) }
    var pager by remember { mutableStateOf<Pair<List<FileEntry>, Int>?>(null) }
    var saving by remember { mutableStateOf<List<FileEntry>>(emptyList()) }
    var favTick by remember { mutableIntStateOf(0) }
    val act = rememberActions()
    val list: FileList? = load.value
    val shown: List<FileEntry> = (if (searching && query.trim().length >= 2) search.value?.entries else list?.entries)?.let { sortEntries(it, order) } ?: emptyList()
    val selection = shown.filter { it.path in selected }
    val writable = list?.writable == true
    val share = shares.value?.filter { path == it.path || path.startsWith(it.path + "/") }?.maxByOrNull { it.path.length }
    fun reload() { Thumbs.retryFailed(); load.reload(); if (searching) search.reload() }

    // Uploads end in the background: refresh the folder when one finishes.
    LaunchedEffect(path) {
        WorkManager.getInstance(ctx).getWorkInfosByTagFlow("upload").collect { infos ->
            if (infos.any { it.state == WorkInfo.State.SUCCEEDED }) load.reload()
        }
    }

    pager?.let { (items, i) ->
        ImagePager(items.map { PagerImage(it.path, it.name, thumbOf(it, 2048)!!, subtitle = formatDate(it.modTime)) }, i, onClose = { pager = null }) { cur ->
            val e = items.first { it.path == cur.path }
            IconButton(onClick = { pager = null; linkFor = e }) { Icon(Icons.Default.Link, stringResource(R.string.a_share_link)) }
            IconButton(onClick = {
                scope.launch { runCatching { withContext(Dispatchers.IO) { localCopy(ctx, e) } }.onSuccess { shareFile(ctx, it) }.onFailure { toast(ctx, errorText(ctx, it)) } }
            }) { Icon(Icons.Default.Share, stringResource(R.string.share)) }
            IconButton(onClick = { pager = null; deleting = listOf(e) }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete)) }
        }
        return
    }

    fun fetch(e: FileEntry, then: (File) -> Unit) {
        scope.launch {
            progress = e.name to 0f
            try {
                then(withContext(Dispatchers.IO) { localCopy(ctx, e) { p -> progress = e.name to p } })
            } catch (ex: Exception) {
                toast(ctx, errorText(ctx, ex))
            } finally {
                progress = null
            }
        }
    }

    fun open(e: FileEntry) {
        if (e.dir) {
            searching = false
            setPath(e.path)
            return
        }
        when (kindOf(e.name)) {
            Kind.VIDEO -> PlayerActivity.start(ctx, listOf(QueueItem(e.path, e.name, true)), 0)
            Kind.AUDIO -> {
                val songs = shown.filter { !it.dir && kindOf(it.name) == Kind.AUDIO }
                PlayerActivity.start(ctx, songs.map { QueueItem(it.path, it.name, false) }, songs.indexOf(e).coerceAtLeast(0))
            }
            Kind.IMAGE -> {
                val pics = shown.filter { !it.dir && kindOf(it.name) == Kind.IMAGE }
                pager = pics to pics.indexOf(e).coerceAtLeast(0)
            }
            Kind.PDF -> fetch(e) { showPdf(e, it) }
            Kind.OTHER -> fetch(e) { openWith(ctx, it) }
        }
    }

    fun setFavorite(items: List<FileEntry>, offline: Boolean) {
        items.forEach { e ->
            val old = favorites.get(e.path)
            favorites.put(Favorite(e.path, e.name, e.dir, offline = offline && !e.dir, size = old?.size ?: 0, modTime = old?.modTime ?: ""))
        }
        if (offline) {
            OfflineSyncWorker.syncNow(ctx)
            OfflineSyncWorker.schedule(ctx)
        }
        favTick++
        toast(ctx, res.getString(if (offline) R.string.a_offline_added else R.string.a_favorite_added, items.size))
    }

    val saveOne = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("*/*")) { uri ->
        val e = saving.firstOrNull() ?: return@rememberLauncherForActivityResult
        saving = emptyList()
        if (uri != null) fetch(e) { f ->
            scope.launch {
                runCatching { withContext(Dispatchers.IO) { copyToUri(ctx, f, uri) } }
                    .onSuccess { toast(ctx, res.getString(R.string.saved)) }.onFailure { toast(ctx, errorText(ctx, it)) }
            }
        }
    }
    val saveMany = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { tree ->
        val items = saving
        saving = emptyList()
        if (tree != null && items.isNotEmpty()) scope.launch {
            progress = res.getString(R.string.preparing) to 0f
            runCatching { withContext(Dispatchers.IO) { saveToTree(ctx, tree, items) { i -> progress = items[i].name to i.toFloat() / items.size } } }
                .onSuccess { toast(ctx, res.getString(R.string.saved)) }.onFailure { toast(ctx, errorText(ctx, it)) }
            progress = null
        }
    }
    fun save(items: List<FileEntry>) {
        saving = items
        if (items.size == 1) saveOne.launch(downloadName(items[0])) else saveMany.launch(null)
    }

    val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetMultipleContents()) { uris ->
        val folder = list?.path ?: return@rememberLauncherForActivityResult
        if (uris.isNotEmpty()) scope.launch {
            progress = res.getString(R.string.preparing) to 0f
            val dir = withContext(Dispatchers.IO) { stageUris(ctx, uris) }
            progress = null
            UploadWorker.enqueue(ctx, folder, dir)
            toast(ctx, res.getString(R.string.upload_started, uris.size))
        }
    }

    androidx.activity.compose.BackHandler(enabled = selected.isNotEmpty() || searching || (list != null && list.path != ROOT)) {
        when {
            selected.isNotEmpty() -> selected = emptySet()
            searching -> { searching = false; query = "" }
            else -> setPath(list!!.path.substringBeforeLast('/'))
        }
    }

    Scaffold(
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
        topBar = {
            when {
                selected.isNotEmpty() -> TopAppBar(
                    title = { Text(stringResource(R.string.a_selected, selected.size)) },
                    navigationIcon = { IconButton(onClick = { selected = emptySet() }) { Icon(Icons.Default.Close, stringResource(R.string.cancel)) } },
                    actions = {
                        IconButton(onClick = { selected = shown.map { it.path }.toSet() }) { Icon(Icons.Default.SelectAll, stringResource(R.string.a_select_all)) }
                        IconButton(onClick = { deleting = selection }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete)) }
                        Box {
                            IconButton(onClick = { selMenu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.a_more)) }
                            DropdownMenu(selMenu, { selMenu = false }) {
                                DropdownMenuItem({ Text(stringResource(R.string.a_copy_to)) }, { selMenu = false; copying = selection to false })
                                DropdownMenuItem({ Text(stringResource(R.string.a_move_to)) }, { selMenu = false; copying = selection to true })
                                DropdownMenuItem({ Text(stringResource(R.string.save_to_phone)) }, { selMenu = false; save(selection); selected = emptySet() })
                                DropdownMenuItem({ Text(stringResource(R.string.a_add_favorite)) }, { selMenu = false; setFavorite(selection, false); selected = emptySet() })
                                if (selection.any { !it.dir }) DropdownMenuItem({ Text(stringResource(R.string.a_keep_offline)) }, { selMenu = false; setFavorite(selection.filter { !it.dir }, true); selected = emptySet() })
                                if (selection.size == 1) {
                                    DropdownMenuItem({ Text(stringResource(R.string.a_share_link)) }, { selMenu = false; linkFor = selection[0]; selected = emptySet() })
                                    DropdownMenuItem({ Text(stringResource(R.string.a_rename)) }, { selMenu = false; renaming = selection[0]; selected = emptySet() })
                                    DropdownMenuItem({ Text(stringResource(R.string.a_properties)) }, { selMenu = false; propsFor = selection[0]; selected = emptySet() })
                                }
                            }
                        }
                    },
                )
                searching -> TopAppBar(
                    title = {
                        TextField(query, { query = it }, placeholder = { Text(stringResource(R.string.a_search_here)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
                            colors = TextFieldDefaults.colors(focusedContainerColor = Color.Transparent, unfocusedContainerColor = Color.Transparent))
                    },
                    navigationIcon = { IconButton(onClick = { searching = false; query = "" }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
                )
                else -> TopAppBar(
                    title = { Text(list?.path?.let { if (it == ROOT) null else it.substringAfterLast('/') } ?: stringResource(R.string.tab_files), maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    navigationIcon = {
                        if (list != null && list.path != ROOT) IconButton(onClick = { setPath(list.path.substringBeforeLast('/')) }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) }
                    },
                    actions = {
                        IconButton(onClick = { searching = true }) { Icon(Icons.Default.Search, stringResource(R.string.a_search)) }
                        Box {
                            IconButton(onClick = { sortMenu = true }) { Icon(Icons.AutoMirrored.Filled.Sort, stringResource(R.string.a_sort)) }
                            DropdownMenu(sortMenu, { sortMenu = false }) {
                                listOf(SortBy.NAME to R.string.a_sort_name, SortBy.DATE to R.string.a_sort_date, SortBy.SIZE to R.string.a_sort_size, SortBy.TYPE to R.string.a_sort_type).forEach { (by, label) ->
                                    DropdownMenuItem(
                                        text = { Text(stringResource(label)) },
                                        leadingIcon = { if (order.by == by) Icon(Icons.Default.CheckCircle, null) },
                                        onClick = { order = if (order.by == by) order.copy(descending = !order.descending) else FileOrder(by, by == SortBy.DATE || by == SortBy.SIZE); prefs.order = order; sortMenu = false },
                                    )
                                }
                                HorizontalDivider()
                                DropdownMenuItem({ Text(stringResource(if (order.descending) R.string.a_descending else R.string.a_ascending)) }, { order = order.copy(descending = !order.descending); prefs.order = order; sortMenu = false })
                            }
                        }
                        IconButton(onClick = { grid = !grid; prefs.grid = grid }) {
                            Icon(if (grid) Icons.AutoMirrored.Filled.ViewList else Icons.Default.GridView, stringResource(if (grid) R.string.a_list_view else R.string.a_grid_view))
                        }
                        Box {
                            IconButton(onClick = { moreMenu = true }) { Icon(Icons.Default.MoreVert, stringResource(R.string.a_more)) }
                            DropdownMenu(moreMenu, { moreMenu = false }) {
                                if (writable && !inRecycle) DropdownMenuItem({ Text(stringResource(R.string.new_folder)) }, { moreMenu = false; newFolder = true }, leadingIcon = { Icon(Icons.Default.CreateNewFolder, null) })
                                DropdownMenuItem({ Text(stringResource(R.string.a_favorites)) }, { moreMenu = false; goPlace("favorites") }, leadingIcon = { Icon(Icons.Default.Star, null) })
                                DropdownMenuItem({ Text(stringResource(R.string.a_share_links)) }, { moreMenu = false; goPlace("links") })
                                if (share?.recycleBin == true && !inRecycle) DropdownMenuItem({ Text(stringResource(R.string.a_recycle_bin)) }, { moreMenu = false; setPath(share.path + "/.recycle") }, leadingIcon = { Icon(Icons.Default.Delete, null) })
                            }
                        }
                        accountAction()
                    },
                )
            }
        },
        floatingActionButton = {
            if (writable && selected.isEmpty() && !searching && !inRecycle) FloatingActionButton(onClick = { pick.launch("*/*") }) { Icon(Icons.Default.Upload, stringResource(R.string.upload)) }
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            progress?.let { (name, p) ->
                Text(name, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(horizontal = 16.dp))
                LinearProgressIndicator(progress = { p }, modifier = Modifier.fillMaxWidth().padding(16.dp, 4.dp))
            }
            if (inRecycle) Text(stringResource(R.string.a_recycle_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp, 8.dp))
            if (searching && search.value?.truncated == true) Text(stringResource(R.string.a_search_truncated), style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(16.dp, 4.dp))
            val source = if (searching && query.trim().length >= 2) search else load
            Refreshable(source, Modifier.weight(1f)) { _ ->
                @Suppress("UNUSED_EXPRESSION") favTick
                val click = { e: FileEntry -> if (selected.isNotEmpty()) selected = if (e.path in selected) selected - e.path else selected + e.path else open(e) }
                val long = { e: FileEntry -> selected = selected + e.path }
                when {
                    shown.isEmpty() && searching && query.trim().length < 2 -> EmptyState(Icons.Default.Search, stringResource(R.string.a_search_hint))
                    shown.isEmpty() -> EmptyState(if (searching) Icons.Default.Search else Icons.Default.FolderOff, stringResource(if (searching) R.string.a_no_results else R.string.empty_folder))
                    grid -> LazyVerticalGrid(GridCells.Adaptive(112.dp), Modifier.fillMaxSize(), contentPadding = androidx.compose.foundation.layout.PaddingValues(8.dp)) {
                        items(shown, key = { it.path }) { e ->
                            Column(Modifier.padding(4.dp).combinedClickable(onClick = { click(e) }, onLongClick = { long(e) }), horizontalAlignment = Alignment.CenterHorizontally) {
                                Box {
                                    NasImage(thumbOf(e, 256), e.name, Modifier.fillMaxWidth().aspectRatio(1f).clip(RoundedCornerShape(10.dp)), maxPx = 256, fallback = iconOf(e))
                                    if (e.path in selected) Icon(Icons.Default.CheckCircle, stringResource(R.string.a_selected_one), Modifier.align(Alignment.TopEnd).padding(4.dp), tint = MaterialTheme.colorScheme.primary)
                                }
                                Text(e.name, style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 4.dp))
                            }
                        }
                    }
                    else -> LazyColumn(Modifier.fillMaxSize()) {
                        items(shown, key = { it.path }) { e ->
                            val fav = favorites.get(e.path)
                            ListItem(
                                headlineContent = { Text(e.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                                supportingContent = {
                                    Text(buildList {
                                        if (searching) add(displayPath(e.path.substringBeforeLast('/')))
                                        if (!e.dir) add(formatBytes(e.size))
                                        add(formatDate(e.modTime))
                                    }.joinToString(" · "), maxLines = 1, overflow = TextOverflow.Ellipsis)
                                },
                                leadingContent = {
                                    if (selected.isNotEmpty()) Checkbox(e.path in selected, { click(e) })
                                    else NasImage(thumbOf(e, 128), null, Modifier.size(40.dp).clip(RoundedCornerShape(6.dp)), maxPx = 128, fallback = iconOf(e))
                                },
                                trailingContent = {
                                    Box {
                                        androidx.compose.foundation.layout.Row(verticalAlignment = Alignment.CenterVertically) {
                                            if (fav?.offline == true) Icon(Icons.Default.DownloadDone, stringResource(R.string.a_offline_kept), Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary)
                                            else if (fav != null) Icon(Icons.Default.Star, stringResource(R.string.a_favorite), Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary)
                                            if (selected.isEmpty()) IconButton(onClick = { menuFor = e }) { Icon(Icons.Default.MoreVert, stringResource(R.string.a_actions_for, e.name)) }
                                        }
                                        DropdownMenu(menuFor == e, { menuFor = null }) {
                                            val close = { menuFor = null }
                                            if (inRecycle) {
                                                recycleOrigin(e.path)?.let { to ->
                                                    DropdownMenuItem({ Text(stringResource(R.string.a_restore)) }, { close(); act.run(load.reload) { api().renameFile(e.path, to) } })
                                                }
                                                DropdownMenuItem({ Text(stringResource(R.string.a_delete_forever)) }, { close(); deleting = listOf(e) })
                                                return@DropdownMenu
                                            }
                                            if (!e.dir) DropdownMenuItem({ Text(stringResource(R.string.open_with)) }, { close(); fetch(e) { openWith(ctx, it) } })
                                            if (!e.dir) DropdownMenuItem({ Text(stringResource(R.string.share)) }, { close(); fetch(e) { shareFile(ctx, it) } })
                                            DropdownMenuItem({ Text(stringResource(R.string.a_share_link)) }, { close(); linkFor = e })
                                            DropdownMenuItem({ Text(stringResource(R.string.save_to_phone)) }, { close(); save(listOf(e)) })
                                            DropdownMenuItem({ Text(stringResource(R.string.a_rename)) }, { close(); renaming = e })
                                            DropdownMenuItem({ Text(stringResource(R.string.a_copy_to)) }, { close(); copying = listOf(e) to false })
                                            DropdownMenuItem({ Text(stringResource(R.string.a_move_to)) }, { close(); copying = listOf(e) to true })
                                            if (fav == null) DropdownMenuItem({ Text(stringResource(R.string.a_add_favorite)) }, { close(); setFavorite(listOf(e), false) })
                                            else DropdownMenuItem({ Text(stringResource(R.string.a_unfavorite)) }, { close(); favorites.remove(e.path); favTick++ })
                                            if (!e.dir && fav?.offline != true) DropdownMenuItem({ Text(stringResource(R.string.a_keep_offline)) }, { close(); setFavorite(listOf(e), true) })
                                            DropdownMenuItem({ Text(stringResource(R.string.a_properties)) }, { close(); propsFor = e })
                                            DropdownMenuItem({ Text(stringResource(R.string.a_delete), color = MaterialTheme.colorScheme.error) }, { close(); deleting = listOf(e) })
                                        }
                                    }
                                },
                                modifier = Modifier.combinedClickable(onClick = { click(e) }, onLongClick = { long(e) }),
                            )
                            HorizontalDivider()
                        }
                    }
                }
            }
        }
    }

    if (newFolder) TextDialog(stringResource(R.string.new_folder), stringResource(R.string.name), confirm = stringResource(R.string.create),
        valid = { it.isNotBlank() && !it.contains('/') }, onDismiss = { newFolder = false }) { name ->
        act.run(load.reload) { api().mkdir(list!!.path + "/" + name) }
    }
    renaming?.let { e ->
        TextDialog(stringResource(R.string.a_rename), stringResource(R.string.name), initial = e.name, confirm = stringResource(R.string.a_rename),
            valid = { it.isNotBlank() && !it.contains('/') && it != e.name }, onDismiss = { renaming = null }) { name ->
            act.run(::reload) { api().renameFile(e.path, e.path.substringBeforeLast('/') + "/" + name) }
        }
    }
    deleting?.let { items ->
        val permanent = inRecycle || share?.recycleBin != true
        ConfirmDialog(
            title = stringResource(R.string.a_delete_n, items.size),
            text = stringResource(if (permanent) R.string.a_delete_permanent else R.string.a_delete_to_bin),
            confirm = stringResource(R.string.a_delete), danger = true,
            details = items.take(8).map { it.name } + if (items.size > 8) listOf("…") else emptyList(),
            onDismiss = { deleting = null },
            onConfirm = {
                selected = emptySet()
                act.run(::reload) {
                    val r = api().trashFiles(items.map { it.path })
                    items.forEach { favorites.remove(it.path) }
                    withContext(Dispatchers.Main) {
                        toast(ctx, if (r.recycled > 0) res.getString(R.string.a_moved_to_bin, r.recycled) else res.getString(R.string.a_deleted_n, r.deleted))
                    }
                }
            },
        )
    }
    copying?.let { (items, move) ->
        FolderPicker(stringResource(if (move) R.string.a_move_to else R.string.a_copy_to), start = path, onPick = { to ->
            copying = null
            selected = emptySet()
            act.job(::reload) { api().copyFiles(items.map { it.path }, to, move) }
        }, onCancel = { copying = null })
    }
    linkFor?.let { e -> ShareLinkDialog(path = e.path, dir = e.dir, writable = writable, title = e.name) { linkFor = null } }
    propsFor?.let { e -> PropertiesDialog(e) { propsFor = null } }
}
