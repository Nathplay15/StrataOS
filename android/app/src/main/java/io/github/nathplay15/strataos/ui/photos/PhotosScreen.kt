package io.github.nathplay15.strataos.ui.photos

import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.itemsIndexed
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.PhotoAlbum
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material.icons.filled.PlayCircle
import androidx.compose.material.icons.filled.PlaylistAdd
import androidx.compose.material.icons.filled.RemoveCircleOutline
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Share
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.Album
import io.github.nathplay15.strataos.net.FileEntry
import io.github.nathplay15.strataos.net.Photo
import io.github.nathplay15.strataos.net.albums
import io.github.nathplay15.strataos.net.changeAlbum
import io.github.nathplay15.strataos.net.createAlbum
import io.github.nathplay15.strataos.net.deleteAlbum
import io.github.nathplay15.strataos.net.photoThumbUrl
import io.github.nathplay15.strataos.net.photos
import io.github.nathplay15.strataos.net.trashFiles
import io.github.nathplay15.strataos.ui.Chip
import io.github.nathplay15.strataos.ui.ConfirmDialog
import io.github.nathplay15.strataos.ui.EmptyState
import io.github.nathplay15.strataos.ui.ErrorPane
import io.github.nathplay15.strataos.ui.NasImage
import io.github.nathplay15.strataos.ui.Refreshable
import io.github.nathplay15.strataos.ui.TextDialog
import io.github.nathplay15.strataos.ui.Thumbs
import io.github.nathplay15.strataos.ui.Tone
import io.github.nathplay15.strataos.ui.api
import io.github.nathplay15.strataos.ui.errorText
import io.github.nathplay15.strataos.ui.files.ShareLinkDialog
import io.github.nathplay15.strataos.ui.files.copyToUri
import io.github.nathplay15.strataos.ui.files.localCopy
import io.github.nathplay15.strataos.ui.files.saveToTree
import io.github.nathplay15.strataos.ui.player.PlayerActivity
import io.github.nathplay15.strataos.ui.player.QueueItem
import io.github.nathplay15.strataos.ui.rememberActions
import io.github.nathplay15.strataos.ui.rememberLoad
import io.github.nathplay15.strataos.ui.shareFile
import io.github.nathplay15.strataos.ui.toast
import java.time.Instant
import java.time.YearMonth
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.util.Locale
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private const val PAGE = 300

/** Photos grouped by the month they were taken, newest first (the order the NAS sends). */
fun groupByMonth(items: List<Photo>, zone: ZoneId = ZoneId.systemDefault()): List<Pair<YearMonth, List<Photo>>> {
    val out = LinkedHashMap<YearMonth, MutableList<Photo>>()
    for (p in items) out.getOrPut(YearMonth.from(Instant.ofEpochSecond(p.date).atZone(zone))) { mutableListOf() }.add(p)
    return out.map { it.key to it.value }
}

private fun Photo.entry() = FileEntry(name, path, false, size, "")

private fun thumb(p: Photo, size: String) = api().photoThumbUrl(p.path, size).newBuilder().addQueryParameter("v", p.mtime.toString()).build()

/** The Photos tab: timeline and albums of the NAS Photos app. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PhotosScreen(accountAction: @Composable () -> Unit) {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    var album by remember { mutableStateOf<Album?>(null) }
    album?.let { a ->
        BackHandler { album = null }
        PhotoGrid(album = a, accountAction = {}, onBack = { album = null }, onAlbumGone = { album = null })
        return
    }
    Column(Modifier.fillMaxSize()) {
        if (tab == 0) PhotoGrid(album = null, accountAction = accountAction, tabs = { Tabs(tab) { tab = it } })
        else {
            TopAppBar(title = { Text(stringResource(R.string.a_photos)) }, actions = { accountAction() })
            Tabs(tab) { tab = it }
            AlbumsList { album = it }
        }
    }
}

@Composable
private fun Tabs(tab: Int, onTab: (Int) -> Unit) {
    PrimaryTabRow(selectedTabIndex = tab) {
        Tab(tab == 0, { onTab(0) }, text = { Text(stringResource(R.string.a_timeline)) })
        Tab(tab == 1, { onTab(1) }, text = { Text(stringResource(R.string.a_albums)) })
    }
}

/** Timeline (or one album): month sections, endless scrolling, selection, viewer. */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalFoundationApi::class)
@Composable
private fun PhotoGrid(album: Album?, accountAction: @Composable () -> Unit, tabs: @Composable () -> Unit = {}, onBack: (() -> Unit)? = null, onAlbumGone: () -> Unit = {}) {
    val ctx = LocalContext.current
    val res = androidx.compose.ui.platform.LocalResources.current
    val scope = rememberCoroutineScope()
    val act = rememberActions()
    var query by rememberSaveable { mutableStateOf("") }
    var searching by rememberSaveable { mutableStateOf(false) }
    var reloadTick by remember { mutableIntStateOf(0) }
    val loaded = remember(album?.id, query, reloadTick) { mutableStateOf<List<Photo>>(emptyList()) }
    var total by remember(album?.id, query, reloadTick) { mutableIntStateOf(-1) }
    var installed by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<Throwable?>(null) }
    var loading by remember { mutableStateOf(false) }
    var refreshing by remember { mutableStateOf(false) }
    var selected by remember(album?.id) { mutableStateOf(setOf<String>()) }
    var viewing by remember { mutableStateOf<Int?>(null) }
    var addTo by remember { mutableStateOf<List<String>?>(null) }
    var deleting by remember { mutableStateOf<List<Photo>?>(null) }
    var renaming by remember { mutableStateOf(false) }
    var sharing by remember { mutableStateOf(false) }
    var deletingAlbum by remember { mutableStateOf(false) }
    var name by remember(album?.id) { mutableStateOf(album?.name ?: "") }
    var saving by remember { mutableStateOf<List<Photo>>(emptyList()) }
    val grid = rememberLazyGridState()

    suspend fun more() {
        if (loading || (total >= 0 && loaded.value.size >= total)) return
        loading = true
        try {
            val page = withContext(Dispatchers.IO) { api().photos(loaded.value.size, PAGE, album?.id ?: "", if (searching) query else "") }
            installed = page.installed
            total = page.total
            loaded.value = loaded.value + page.items
            error = null
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            error = e
        } finally {
            loading = false
        }
    }
    LaunchedEffect(album?.id, query, reloadTick) {
        if (searching && query.isNotEmpty()) delay(400)
        more()
        refreshing = false
    }
    LaunchedEffect(grid, loaded.value.size) {
        snapshotFlow { grid.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0 }.collect { last ->
            if (last >= grid.layoutInfo.totalItemsCount - 40) more()
        }
    }
    val list = loaded.value
    val selection = list.filter { it.path in selected }

    viewing?.let { start ->
        ImagePager(list.map { PagerImage(it.path, it.name, thumb(it, "l"), it.video, formatPhotoDate(it.date)) }, start, onClose = { viewing = null },
            onPlay = { p -> PlayerActivity.start(ctx, listOf(QueueItem(p.path, p.name, true)), 0) }) { cur ->
            val p = list.first { it.path == cur.path }
            IconButton(onClick = {
                scope.launch { runCatching { withContext(Dispatchers.IO) { localCopy(ctx, p.entry()) } }.onSuccess { shareFile(ctx, it) }.onFailure { toast(ctx, errorText(ctx, it)) } }
            }) { Icon(Icons.Default.Share, stringResource(R.string.share)) }
            IconButton(onClick = { addTo = listOf(p.path) }) { Icon(Icons.Default.PlaylistAdd, stringResource(R.string.a_add_to_album)) }
            IconButton(onClick = { saving = listOf(p) }) { Icon(Icons.Default.Download, stringResource(R.string.save_to_phone)) }
            IconButton(onClick = { viewing = null; deleting = listOf(p) }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete)) }
        }
        if (addTo != null) AddToAlbumDialog(addTo!!) { addTo = null }
        if (saving.isNotEmpty()) SaveLauncher(saving) { saving = emptyList() }
        return
    }

    BackHandler(enabled = selected.isNotEmpty() || searching) {
        if (selected.isNotEmpty()) selected = emptySet() else { searching = false; query = "" }
    }

    Scaffold(
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
        topBar = {
            Column {
                when {
                    selected.isNotEmpty() -> TopAppBar(
                        title = { Text(stringResource(R.string.a_selected, selected.size)) },
                        navigationIcon = { IconButton(onClick = { selected = emptySet() }) { Icon(Icons.Default.Close, stringResource(R.string.cancel)) } },
                        actions = {
                            IconButton(onClick = { addTo = selected.toList() }) { Icon(Icons.Default.PlaylistAdd, stringResource(R.string.a_add_to_album)) }
                            if (album != null) IconButton(onClick = {
                                val gone = selected.toList()
                                act.run({ selected = emptySet(); reloadTick++ }) { api().changeAlbum(album.id, remove = gone) }
                            }) { Icon(Icons.Default.RemoveCircleOutline, stringResource(R.string.a_remove_from_album)) }
                            IconButton(onClick = { saving = selection }) { Icon(Icons.Default.Download, stringResource(R.string.save_to_phone)) }
                            IconButton(onClick = { deleting = selection }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete)) }
                        },
                    )
                    searching -> TopAppBar(
                        title = {
                            TextField(query, { query = it }, placeholder = { Text(stringResource(R.string.a_search_photos)) }, singleLine = true, modifier = Modifier.fillMaxWidth(),
                                colors = TextFieldDefaults.colors(focusedContainerColor = Color.Transparent, unfocusedContainerColor = Color.Transparent))
                        },
                        navigationIcon = { IconButton(onClick = { searching = false; query = "" }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
                    )
                    else -> TopAppBar(
                        title = { Text(album?.let { name } ?: stringResource(R.string.a_photos), maxLines = 1, overflow = TextOverflow.Ellipsis) },
                        navigationIcon = { if (onBack != null) IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } },
                        actions = {
                            IconButton(onClick = { searching = true }) { Icon(Icons.Default.Search, stringResource(R.string.a_search)) }
                            if (album != null) {
                                IconButton(onClick = { sharing = true }) { Icon(Icons.Default.Link, stringResource(R.string.a_share_link)) }
                                IconButton(onClick = { renaming = true }) { Icon(Icons.Default.Edit, stringResource(R.string.a_rename)) }
                                IconButton(onClick = { deletingAlbum = true }) { Icon(Icons.Default.Delete, stringResource(R.string.a_delete_album)) }
                            }
                            accountAction()
                        },
                    )
                }
                if (selected.isEmpty() && !searching) tabs()
            }
        },
    ) { pad ->
        PullToRefreshBox(isRefreshing = refreshing, onRefresh = { refreshing = true; Thumbs.retryFailed(); reloadTick++ }, modifier = Modifier.padding(pad).fillMaxSize()) {
            when {
                error != null && list.isEmpty() -> ErrorPane(error!!) { reloadTick++ }
                !installed -> EmptyState(Icons.Default.PhotoLibrary, stringResource(R.string.a_photos_not_installed))
                total == 0 -> EmptyState(Icons.Default.PhotoLibrary, stringResource(when {
                    searching -> R.string.a_no_results
                    album != null -> R.string.a_album_empty
                    else -> R.string.a_no_photos
                }))
                total < 0 -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { androidx.compose.material3.CircularProgressIndicator() }
                else -> { val locale = androidx.compose.ui.platform.LocalLocale.current.platformLocale
                LazyVerticalGrid(GridCells.Adaptive(100.dp), Modifier.fillMaxSize(), state = grid, contentPadding = PaddingValues(2.dp)) {
                    var index = 0
                    for ((month, photos) in groupByMonth(list)) {
                        item(key = "m$month", span = { GridItemSpan(maxLineSpan) }) {
                            Text(month.format(DateTimeFormatter.ofPattern("LLLL yyyy", locale)).replaceFirstChar { it.titlecase() },
                                style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(12.dp, 16.dp, 12.dp, 6.dp))
                        }
                        val base = index
                        itemsIndexed(photos, key = { _, it -> it.path }) { j, p ->
                            val i = base + j
                            Box(Modifier.padding(2.dp).aspectRatio(1f).combinedClickable(
                                onClick = { if (selected.isNotEmpty()) selected = if (p.path in selected) selected - p.path else selected + p.path else viewing = i },
                                onLongClick = { selected = selected + p.path },
                            )) {
                                NasImage(thumb(p, "s"), p.name, Modifier.fillMaxSize().clip(RoundedCornerShape(4.dp)), maxPx = 360)
                                if (p.video) Icon(Icons.Default.PlayCircle, stringResource(R.string.a_video), Modifier.align(Alignment.Center).size(32.dp), tint = Color.White)
                                if (p.path in selected) Icon(Icons.Default.CheckCircle, stringResource(R.string.a_selected_one), Modifier.align(Alignment.TopEnd).padding(4.dp), tint = MaterialTheme.colorScheme.primary)
                            }
                        }
                        index += photos.size
                    }
                }
                }
            }
        }
    }

    if (addTo != null) AddToAlbumDialog(addTo!!) { addTo = null; selected = emptySet() }
    if (saving.isNotEmpty()) SaveLauncher(saving) { saving = emptyList(); selected = emptySet() }
    deleting?.let { ps ->
        ConfirmDialog(stringResource(R.string.a_delete_n, ps.size), stringResource(R.string.a_delete_photos_text), stringResource(R.string.a_delete), danger = true,
            details = ps.take(6).map { it.name }, onDismiss = { deleting = null },
            onConfirm = {
                act.run({ selected = emptySet(); reloadTick++ }) {
                    val r = api().trashFiles(ps.map { it.path })
                    withContext(Dispatchers.Main) { toast(ctx, if (r.recycled > 0) res.getString(R.string.a_moved_to_bin, r.recycled) else res.getString(R.string.a_deleted_n, r.deleted)) }
                }
            })
    }
    if (album != null) {
        if (renaming) TextDialog(stringResource(R.string.a_rename), stringResource(R.string.name), initial = name, confirm = stringResource(R.string.a_rename), onDismiss = { renaming = false }) { n ->
            act.run({ name = n }) { api().changeAlbum(album.id, name = n) }
        }
        if (sharing) ShareLinkDialog(album = album.id, title = name) { sharing = false }
        if (deletingAlbum) ConfirmDialog(stringResource(R.string.a_delete_album), stringResource(R.string.a_delete_album_text, name), stringResource(R.string.a_delete), danger = true,
            onDismiss = { deletingAlbum = false }, onConfirm = { act.run(onAlbumGone) { api().deleteAlbum(album.id) } })
    }
}

fun formatPhotoDate(unix: Long): String = runCatching {
    DateTimeFormatter.ofLocalizedDateTime(FormatStyle.MEDIUM, FormatStyle.SHORT).format(Instant.ofEpochSecond(unix).atZone(ZoneId.systemDefault()))
}.getOrDefault("")

/** Saves photos to the phone (one: a file to name; several: a folder to choose). */
@Composable
private fun SaveLauncher(items: List<Photo>, done: () -> Unit) {
    val ctx = LocalContext.current
    val res = androidx.compose.ui.platform.LocalResources.current
    val scope = rememberCoroutineScope()
    val one = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("*/*")) { uri ->
        if (uri != null) scope.launch {
            runCatching { withContext(Dispatchers.IO) { copyToUri(ctx, localCopy(ctx, items[0].entry()), uri) } }
                .onSuccess { toast(ctx, res.getString(R.string.saved)) }.onFailure { toast(ctx, errorText(ctx, it)) }
        }
        done()
    }
    val many = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { tree ->
        if (tree != null) scope.launch {
            runCatching { withContext(Dispatchers.IO) { saveToTree(ctx, tree, items.map { it.entry() }) {} } }
                .onSuccess { toast(ctx, res.getString(R.string.saved)) }.onFailure { toast(ctx, errorText(ctx, it)) }
        }
        done()
    }
    LaunchedEffect(items) { if (items.size == 1) one.launch(items[0].name) else many.launch(null) }
}

/** Adds photos to an album, or to a new one. */
@Composable
private fun AddToAlbumDialog(paths: List<String>, onDone: () -> Unit) {
    val ctx = LocalContext.current
    val res = androidx.compose.ui.platform.LocalResources.current
    val act = rememberActions()
    val load = rememberLoad(Unit) { api().albums() }
    var creating by remember { mutableStateOf(false) }
    if (creating) {
        TextDialog(stringResource(R.string.a_new_album), stringResource(R.string.name), confirm = stringResource(R.string.create), onDismiss = { creating = false; onDone() }) { n ->
            act.run({ toast(ctx, res.getString(R.string.a_added_to_album, paths.size, n)) }) { api().createAlbum(n, paths) }
        }
        return
    }
    AlertDialog(
        onDismissRequest = onDone,
        title = { Text(stringResource(R.string.a_add_to_album)) },
        text = {
            Box(Modifier.fillMaxWidth()) {
                Refreshable(load, Modifier.padding(0.dp)) { albums ->
                    LazyColumn {
                        item { ListItem(headlineContent = { Text(stringResource(R.string.a_new_album)) }, leadingContent = { Icon(Icons.Default.Add, null) }, modifier = Modifier.clickable { creating = true }) }
                        items(albums, key = { it.id }) { a ->
                            ListItem(headlineContent = { Text(a.name) }, supportingContent = { Text(stringResource(R.string.a_photo_count, a.count)) },
                                leadingContent = { Icon(Icons.Default.PhotoAlbum, null) },
                                modifier = Modifier.clickable {
                                    act.run({ toast(ctx, res.getString(R.string.a_added_to_album, paths.size, a.name)); onDone() }) { api().changeAlbum(a.id, add = paths) }
                                })
                        }
                    }
                }
            }
        },
        confirmButton = { TextButton(onClick = onDone) { Text(stringResource(R.string.cancel)) } },
    )
}

/** The albums of the user, with their cover. */
@Composable
private fun AlbumsList(open: (Album) -> Unit) {
    val act = rememberActions()
    val load = rememberLoad(Unit) { api().albums() }
    var creating by remember { mutableStateOf(false) }
    Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), floatingActionButton = {
        FloatingActionButton(onClick = { creating = true }) { Icon(Icons.Default.Add, stringResource(R.string.a_new_album)) }
    }) { pad ->
        Refreshable(load, Modifier.padding(pad)) { albums ->
            if (albums.isEmpty()) EmptyState(Icons.Default.PhotoAlbum, stringResource(R.string.a_no_albums))
            else LazyVerticalGrid(GridCells.Adaptive(160.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(8.dp)) {
                items(albums, key = { it.id }) { a ->
                    Column(Modifier.padding(6.dp).clickable { open(a) }) {
                        NasImage(if (a.cover.isNotEmpty()) api().photoThumbUrl(a.cover, "s") else null, a.name,
                            Modifier.fillMaxWidth().aspectRatio(1f).clip(RoundedCornerShape(12.dp)), maxPx = 360, fallback = Icons.Default.PhotoAlbum)
                        Text(a.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 6.dp))
                        androidx.compose.foundation.layout.Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(stringResource(R.string.a_photo_count, a.count), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            if (a.shared) Box(Modifier.padding(start = 6.dp)) { Chip(stringResource(R.string.a_shared), Tone.ACCENT) }
                        }
                    }
                }
            }
        }
    }
    if (creating) TextDialog(stringResource(R.string.a_new_album), stringResource(R.string.name), confirm = stringResource(R.string.create), onDismiss = { creating = false }) { n ->
        act.run(load.reload) { api().createAlbum(n, emptyList()) }
    }
}
