package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R

/** Picks a folder of the NAS the user may write to. */
@Composable
fun FolderPicker(title: String, start: String = "", onPick: (String) -> Unit, onCancel: () -> Unit) {
    var path by remember { mutableStateOf(start) }
    val loader = rememberLoader(path) { Graph.api()!!.files(path) }
    val list = (loader.state as? Loaded.Ok)?.value
    AlertDialog(
        onDismissRequest = onCancel,
        title = { Text(title) },
        text = {
            Column {
                Text(list?.path?.removePrefix("/srv/strata")?.ifEmpty { "/" } ?: "…", style = MaterialTheme.typography.labelLarge)
                Column(Modifier.heightIn(min = 120.dp, max = 400.dp)) {
                    LoadedContent(loader) { l ->
                        LazyColumn {
                            if (l.path != "/srv/strata") item {
                                ListItem(headlineContent = { Text("..") }, leadingContent = { Icon(Icons.AutoMirrored.Filled.ArrowBack, null) },
                                    modifier = Modifier.clickable { path = l.path.substringBeforeLast('/') })
                            }
                            items(l.entries.filter { it.dir }, key = { it.path }) { e ->
                                ListItem(headlineContent = { Text(e.name) }, leadingContent = { Icon(Icons.Default.Folder, null) },
                                    modifier = Modifier.fillMaxWidth().clickable { path = e.path })
                            }
                        }
                    }
                }
                if (list != null && !list.writable) Text(stringResource(R.string.folder_read_only), color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = { TextButton(enabled = list?.writable == true, onClick = { onPick(list!!.path) }) { Text(stringResource(R.string.choose_folder)) } },
        dismissButton = { TextButton(onClick = onCancel) { Text(stringResource(R.string.cancel)) } },
    )
}
