package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import io.github.nathplay15.strataos.net.manage.send
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Movie
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.SkipNext
import androidx.compose.material.icons.filled.SkipPrevious
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Slider
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.d
import io.github.nathplay15.strataos.net.manage.json
import io.github.nathplay15.strataos.net.manage.l
import io.github.nathplay15.strataos.net.manage.o
import io.github.nathplay15.strataos.net.manage.objs
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.net.manage.sendObj
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.AuthImage
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.StatusBadge
import io.github.nathplay15.strataos.ui.adminkit.Tone
import io.github.nathplay15.strataos.ui.adminkit.api
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.player.Playback
import io.github.nathplay15.strataos.ui.player.PlayerActivity
import io.github.nathplay15.strataos.ui.player.QueueItem
import kotlinx.serialization.json.JsonObject

// Cinema (the films and series the NAS found, with their posters and the
// positions it keeps per account) played by the app's own player, which
// streams from the NAS and reports the position back; and the music of the
// NAS (OwnTone, through the NAS's proxy): what plays, the speakers, volume.

/** A film, or a series with its episodes (the web's "work"). */
private class Work(val title: String, val year: Long, val tmdb: Long, val series: Boolean, val items: List<JsonObject>)

private fun worksOf(items: List<JsonObject>): List<Work> {
    val films = items.filter { it.s("kind") != "episode" }.map { Work(it.s("title"), it.l("year"), it.l("tmdbId"), false, listOf(it)) }
    val series = items.filter { it.s("kind") == "episode" }.groupBy { it.s("title") }.map { (t, eps) ->
        Work(t, eps.first().l("year"), eps.first().l("tmdbId"), true, eps.sortedWith(compareBy({ it.l("season") }, { it.l("episode") })))
    }
    return (films + series).sortedBy { it.title.lowercase() }
}

private fun epLabel(i: JsonObject) = "S%02dE%02d".format(i.l("season"), i.l("episode"))

@Composable
fun CinemaScreen(onBack: () -> Unit) {
    val admin = Graph.prefs.server?.isAdmin == true
    val data = rememberData(Unit) { it.json("/api/cinema").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    val ctx = LocalContext.current
    var open by remember { mutableStateOf<Work?>(null) }
    var filter by rememberSaveable { mutableStateOf("all") }
    fun play(items: List<JsonObject>, index: Int, positions: JsonObject) {
        items.forEach { Playback.seedCinema(it.s("path"), positions[it.s("path")].o().d("at") ?: 0.0) }
        PlayerActivity.start(ctx, items.map { QueueItem(it.s("path"), it.s("title") + if (it.s("kind") == "episode") " · " + epLabel(it) else "", true) }, index)
    }
    val w = open
    AreaScreen(w?.title ?: stringResource(R.string.ctl_w_media), if (w != null) ({ open = null }) else onBack, actions = {
        if (admin && w == null) androidx.compose.material3.TextButton(enabled = !actions.busy && data.value?.b("running") != true, onClick = { actions.run(done = r.getString(R.string.md_scan_started), after = data.reload) { it.sendObj("POST", "/api/cinema/scan", JsonObject(emptyMap())) } }) {
            Text(stringResource(R.string.md_scan))
        }
    }) { pad ->
        Refreshable(data, pad) { c ->
            val positions = c["positions"].o()
            val works = worksOf(c.objs("items"))
            if (w != null) {
                ScrollColumn {
                    SectionCard {
                        Row(horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                            Poster(w, Modifier.fillMaxWidth(0.35f))
                            Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                                Text(w.title + if (w.year > 0) " (${w.year})" else "", style = MaterialTheme.typography.titleMedium)
                                val first = w.items.first()
                                first.d("rating")?.takeIf { it > 0 }?.let { Text("★ %.1f".format(it), style = MaterialTheme.typography.bodyMedium) }
                                if (first.l("runtime") > 0) Text(r.getString(R.string.md_minutes, first.l("runtime").toInt()), style = MaterialTheme.typography.bodySmall)
                                if (!w.series) Button(onClick = { play(w.items, 0, positions) }) {
                                    Text(stringResource(if ((positions[first.s("path")].o().d("at") ?: 0.0) > 60) R.string.md_resume else R.string.md_play))
                                }
                            }
                        }
                        val ov = w.items.first().s("overview")
                        if (ov.isNotEmpty()) Text(ov, style = MaterialTheme.typography.bodyMedium)
                    }
                    if (w.series) w.items.forEachIndexed { i, ep ->
                        val pos = positions[ep.s("path")].o()
                        SectionCard(modifier = Modifier.clickable { play(w.items, i, positions) }) {
                            CardHead(epLabel(ep), ep.s("path").substringAfterLast('/')) {
                                val dur = pos.d("dur") ?: 0.0
                                val at = pos.d("at") ?: 0.0
                                if (dur > 0 && at >= dur * 0.9) StatusBadge(stringResource(R.string.md_seen), Tone.Ok) else if (at > 60) StatusBadge(stringResource(R.string.md_started), Tone.Accent)
                            }
                        }
                    }
                }
            } else if (works.isEmpty()) ScrollColumn {
                SectionCard {
                    Hint(stringResource(if (c.b("running")) R.string.md_scanning else R.string.md_empty))
                    if (admin && !c.b("keySet")) TmdbKey { key -> actions.run(done = r.getString(R.string.b_saved), after = data.reload) { it.sendObj("PUT", "/api/cinema/settings", JsonObject(emptyMap()).put("tmdbKey" to key)) } }
                }
            } else {
                val shown = when (filter) { "films" -> works.filter { !it.series }; "series" -> works.filter { it.series }; else -> works }
                val resume = c.objs("items").filter { val p = positions[it.s("path")].o(); (p.d("at") ?: 0.0) > 60 && (p.d("at") ?: 0.0) < (p.d("dur") ?: 0.0) * 0.9 }
                    .sortedByDescending { positions[it.s("path")].o().s("upd") }.take(10)
                LazyVerticalGrid(GridCells.Adaptive(110.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(16.dp), horizontalArrangement = Arrangement.spacedBy(10.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    item(span = { GridItemSpan(maxLineSpan) }) {
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            listOf("all" to R.string.md_all, "films" to R.string.md_films, "series" to R.string.md_series).forEach { (k, l) -> FilterChip(filter == k, { filter = k }, label = { Text(stringResource(l)) }) }
                        }
                    }
                    if (resume.isNotEmpty() && filter == "all") {
                        item(span = { GridItemSpan(maxLineSpan) }) { Text(stringResource(R.string.md_continue), style = MaterialTheme.typography.titleMedium) }
                        items(resume, key = { "r" + it.s("path") }) { it0 ->
                            val wk = works.first { x -> x.items.any { it.s("path") == it0.s("path") } }
                            WorkTile(wk, if (wk.series) epLabel(it0) else null) { play(wk.items, wk.items.indexOfFirst { it.s("path") == it0.s("path") }, positions) }
                        }
                        item(span = { GridItemSpan(maxLineSpan) }) { Text(stringResource(R.string.md_library), style = MaterialTheme.typography.titleMedium) }
                    }
                    items(shown, key = { (if (it.series) "s" else "f") + it.title + it.items.first().s("path") }) { wk -> WorkTile(wk, if (wk.series) r.getString(R.string.md_episodes, wk.items.size) else null) { open = wk } }
                }
            }
        }
    }
}

@Composable
private fun Poster(w: Work, modifier: Modifier) {
    val a = remember { runCatching { api() }.getOrNull() }
    Column(modifier.aspectRatio(2f / 3f).clip(MaterialTheme.shapes.medium)) {
        if (w.tmdb > 0 && a != null) AuthImage(a.url("/api/cinema/poster/${w.tmdb}"), w.title, Modifier.fillMaxSize())
        else androidx.compose.foundation.layout.Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surfaceVariant), contentAlignment = Alignment.Center) { Icon(Icons.Default.Movie, null) }
    }
}

@Composable
private fun WorkTile(w: Work, sub: String?, onClick: () -> Unit) {
    Column(Modifier.clickable(onClick = onClick)) {
        Poster(w, Modifier.fillMaxWidth())
        Text(w.title, Modifier.padding(top = 6.dp), style = MaterialTheme.typography.labelLarge, maxLines = 2, overflow = TextOverflow.Ellipsis)
        Text(sub ?: (if (w.year > 0) w.year.toString() else ""), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun TmdbKey(save: (String) -> Unit) {
    var key by remember { mutableStateOf("") }
    Field(stringResource(R.string.md_tmdb_key), key, { key = it.trim() }, secret = true, help = stringResource(R.string.md_tmdb_help))
    OutlinedButton(enabled = key.isNotEmpty(), onClick = { save(key) }) { Text(stringResource(R.string.b_save)) }
}

// ---- Music of the NAS (OwnTone) ----

private const val M = "/api/media/music/proxy/"

@Composable
fun MusicScreen(onBack: () -> Unit) {
    val data = rememberData(Unit, every = 3000) { a ->
        val cfg = a.json("/api/media/music").o()
        if (!cfg.b("installed")) listOf(cfg, null, null, null)
        else listOf(cfg, runCatching { a.json(M + "api/player").o() }.getOrNull(), runCatching { a.json(M + "api/outputs").o() }.getOrNull(), runCatching { a.json(M + "api/queue").o() }.getOrNull())
    }
    val actions = rememberActions()
    AreaScreen(stringResource(R.string.md_music), onBack) { pad ->
        Refreshable(data, pad) { (cfg, player, outputs, queue) ->
            ScrollColumn {
                if (cfg == null || !cfg.b("installed")) { SectionCard { Hint(stringResource(R.string.md_music_missing)) }; return@ScrollColumn }
                val p = player ?: JsonObject(emptyMap())
                val q = queue?.objs("items").orEmpty()
                val cur = q.firstOrNull { it.l("id") == p.l("item_id") }
                SectionCard {
                    CardHead(cur?.s("title") ?: stringResource(R.string.md_nothing_playing), cur?.let { "${it.s("artist")} · ${it.s("album")}" })
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        IconButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.sendObj("PUT", M + "api/player/previous") } }) { Icon(Icons.Default.SkipPrevious, stringResource(R.string.md_previous)) }
                        IconButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.sendObj("PUT", M + if (p.s("state") == "play") "api/player/pause" else "api/player/play") } }) {
                            Icon(if (p.s("state") == "play") Icons.Default.Pause else Icons.Default.PlayArrow, stringResource(R.string.md_play))
                        }
                        IconButton(enabled = !actions.busy, onClick = { actions.run(after = data.reload) { it.sendObj("PUT", M + "api/player/next") } }) { Icon(Icons.Default.SkipNext, stringResource(R.string.md_next)) }
                    }
                    var vol by remember(p.l("volume")) { mutableFloatStateOf(p.l("volume").toFloat()) }
                    Slider(vol, { vol = it }, valueRange = 0f..100f, onValueChangeFinished = { actions.run { it.call(it.send("PUT", M + "api/player/volume", null, "volume" to vol.toInt().toString()), kotlinx.serialization.json.JsonElement.serializer()) } })
                }
                val outs = outputs?.objs("outputs").orEmpty()
                if (outs.isNotEmpty()) SectionCard(stringResource(R.string.md_speakers)) {
                    outs.forEach { o ->
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) { Text(o.s("name"), style = MaterialTheme.typography.bodyLarge); Text(o.s("type"), style = MaterialTheme.typography.bodySmall) }
                            Switch(o.b("selected"), enabled = !actions.busy, onCheckedChange = { on -> actions.run(after = data.reload) { it.sendObj("PUT", M + "api/outputs/${o.s("id")}", JsonObject(emptyMap()).put("selected" to on)) } })
                        }
                    }
                }
                if (q.isNotEmpty()) SectionCard(stringResource(R.string.md_queue)) {
                    q.take(30).forEach { t -> Text("${t.s("title")} — ${t.s("artist")}", style = if (t.l("id") == p.l("item_id")) MaterialTheme.typography.titleSmall else MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis) }
                }
                Hint(stringResource(R.string.md_music_help))
            }
        }
    }
}

// ---- Versions of a file (snapshots that hold it), as in the web's Versions window ----

@Composable
fun FileVersions(path: String) {
    val data = rememberData(path) { it.json("/api/files/versions", "path" to path).o() }
    val actions = rememberActions()
    val r = LocalResources.current
    var replace by remember { mutableStateOf<JsonObject?>(null) }
    val v = data.value ?: return
    val list = v.objs("versions")
    if (list.isEmpty()) return
    Text(stringResource(R.string.fv_versions), Modifier.padding(top = 12.dp, bottom = 4.dp), style = MaterialTheme.typography.titleSmall)
    list.forEach { ver ->
        Column(Modifier.padding(vertical = 4.dp)) {
            Text(whenText(ver.s("modTime")) + " · " + io.github.nathplay15.strataos.ui.formatBytes(ver.l("size")), style = MaterialTheme.typography.bodyMedium)
            Text(ver.s("snapshot"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (v.b("writable")) Row {
                androidx.compose.material3.TextButton(enabled = !actions.busy, onClick = {
                    actions.run { a -> val p = a.sendObj("POST", "/api/files/versions/restore", JsonObject(emptyMap()).put("path" to path, "snapshot" to ver.s("snapshot"), "replace" to false)).o().s("path"); actions.say(r.getString(R.string.fv_restored, p.substringAfterLast('/'))) }
                }) { Text(stringResource(R.string.fv_restore_copy)) }
                androidx.compose.material3.TextButton(enabled = !actions.busy, onClick = { replace = ver }) { Text(stringResource(R.string.fv_replace)) }
            }
        }
    }
    replace?.let { ver ->
        io.github.nathplay15.strataos.ui.adminkit.Confirm(stringResource(R.string.fv_replace_title), stringResource(R.string.fv_replace_text), stringResource(R.string.fv_replace), onDismiss = { replace = null }, onConfirm = {
            actions.run { a -> val p = a.sendObj("POST", "/api/files/versions/restore", JsonObject(emptyMap()).put("path" to path, "snapshot" to ver.s("snapshot"), "replace" to true)).o().s("path"); actions.say(r.getString(R.string.fv_restored, p.substringAfterLast('/'))) }
        })
    }
}
