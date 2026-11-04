package io.github.nathplay15.strataos.ui

import android.content.Context
import android.content.Intent
import android.provider.Settings
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.SystemUpdate
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import androidx.lifecycle.compose.LifecycleResumeEffect
import io.github.nathplay15.strataos.BuildConfig
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.update.AppVersion
import io.github.nathplay15.strataos.update.Releases
import io.github.nathplay15.strataos.update.UpdateException
import io.github.nathplay15.strataos.update.UpdatePrefs
import io.github.nathplay15.strataos.update.UpdateScheduler
import io.github.nathplay15.strataos.update.Updater
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

// Downloads and installations go on when the screen is left.
private val updates = CoroutineScope(SupervisorJob() + Dispatchers.IO)

fun updateErrorText(ctx: Context, e: UpdateException): String = when (e.kind) {
    UpdateException.Kind.NO_APK -> ctx.getString(R.string.update_err_no_apk)
    UpdateException.Kind.NO_SUMS -> ctx.getString(R.string.update_err_no_sums)
    UpdateException.Kind.CHECKSUM -> ctx.getString(R.string.update_err_checksum)
    UpdateException.Kind.SIGNATURE -> ctx.getString(R.string.update_err_signature)
    UpdateException.Kind.PACKAGE -> ctx.getString(R.string.update_err_package)
    UpdateException.Kind.NOT_NEWER -> ctx.getString(R.string.update_err_older)
    UpdateException.Kind.NOT_ALLOWED -> ctx.getString(R.string.update_unknown_sources)
    UpdateException.Kind.CANCELLED -> ctx.getString(R.string.update_err_cancelled)
    UpdateException.Kind.INSTALL -> ctx.getString(R.string.update_err_install, e.detail)
    UpdateException.Kind.NETWORK -> ctx.getString(R.string.update_err_network, e.detail)
}

/** App updates: installed and latest versions, release notes, the update itself and its settings. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun UpdateScreen(onBack: () -> Unit) {
    val ctx = LocalContext.current
    val uri = LocalUriHandler.current
    val p = remember { Updater.prefs(ctx) }
    var tick by remember { mutableIntStateOf(0) }
    val st by Updater.state.collectAsState()
    // Back from the system settings ("Install unknown apps") or from the install confirmation.
    LifecycleResumeEffect(Unit) { tick++; onPauseOrDispose {} }
    val latest = remember(tick, st) { p.latest }
    val canInstall = remember(tick) { Updater.canInstall(ctx) }
    val busy = st.step != Updater.Step.IDLE && st.step != Updater.Step.FAILED
    val installed = Updater.installed
    val newer = latest?.version?.let { Releases.isNewer(it, installed) } == true

    fun check() = updates.launch { Updater.checkNow(ctx); tick++ }
    fun update() {
        val r = latest ?: return
        updates.launch { runCatching { Updater.update(ctx, r) }; tick++ }
    }

    Column(Modifier.fillMaxSize()) {
        TopAppBar(title = { Text(stringResource(R.string.update_title)) }, navigationIcon = { IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.back)) } })
        Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Card(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(stringResource(R.string.update_installed, BuildConfig.VERSION_NAME), style = MaterialTheme.typography.titleMedium)
                    Text(
                        if (latest?.version != null) stringResource(R.string.update_latest, latest.version.toString()) else stringResource(R.string.update_latest_unknown),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    if (p.lastCheck > 0) Text(stringResource(R.string.update_last_check, formatTime(p.lastCheck)), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    if (st.step == Updater.Step.CHECKING) LinearProgressIndicator(Modifier.fillMaxWidth())
                    else if (latest != null && !newer && st.step != Updater.Step.FAILED) Text(stringResource(R.string.update_up_to_date), color = Tone.OK.color())
                    OutlinedButton(onClick = { check() }, enabled = !busy) { Text(stringResource(R.string.update_check)) }
                }
            }

            st.error?.let { Text(updateErrorText(ctx, it), color = MaterialTheme.colorScheme.error) }

            if (latest != null && newer) {
                val files = Releases.files(latest)
                Card(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(stringResource(R.string.update_whats_new, latest.version.toString()), style = MaterialTheme.typography.titleMedium)
                        val notes = Releases.plainNotes(latest.body)
                        if (notes.isNotEmpty()) SelectionContainer { Text(notes, style = MaterialTheme.typography.bodySmall) }
                        if (latest.htmlUrl.startsWith("https://")) TextButton(onClick = { uri.openUri(latest.htmlUrl) }) { Text(stringResource(R.string.update_release_page)) }
                        when {
                            files == null -> Text(stringResource(R.string.update_err_no_apk), color = MaterialTheme.colorScheme.error)
                            !canInstall -> {
                                Text(stringResource(R.string.update_unknown_sources), style = MaterialTheme.typography.bodyMedium)
                                Button(onClick = {
                                    ctx.startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, "package:${ctx.packageName}".toUri()))
                                }) { Text(stringResource(R.string.update_open_settings)) }
                            }
                            st.step == Updater.Step.DOWNLOADING -> {
                                Text(stringResource(R.string.update_downloading, files.apk.name))
                                if (st.progress >= 0) LinearProgressIndicator({ st.progress }, Modifier.fillMaxWidth()) else LinearProgressIndicator(Modifier.fillMaxWidth())
                            }
                            st.step == Updater.Step.VERIFYING || st.step == Updater.Step.INSTALLING -> {
                                Text(stringResource(R.string.update_installing))
                                LinearProgressIndicator(Modifier.fillMaxWidth())
                            }
                            st.step == Updater.Step.WAITING_USER -> {
                                Text(stringResource(R.string.update_waiting))
                                OutlinedButton(onClick = { update() }) { Text(stringResource(R.string.update_install)) }
                            }
                            else -> Button(onClick = { update() }, enabled = !busy) {
                                Icon(Icons.Default.SystemUpdate, null)
                                val ready = Updater.readyApk(ctx, latest) != null
                                Text(
                                    if (ready) stringResource(R.string.update_install) else stringResource(R.string.update_download_install, formatBytes(files.apk.size)),
                                    Modifier.padding(start = 8.dp),
                                )
                            }
                        }
                    }
                }
            }

            HorizontalDivider()
            Text(stringResource(R.string.update_auto_title), style = MaterialTheme.typography.titleMedium)
            listOf(
                Triple(UpdatePrefs.OFF, R.string.update_mode_off, R.string.update_mode_off_hint),
                Triple(UpdatePrefs.NOTIFY, R.string.update_mode_notify, R.string.update_mode_notify_hint),
                Triple(UpdatePrefs.AUTO, R.string.update_mode_auto, R.string.update_mode_auto_hint),
            ).forEach { (mode, label, hint) ->
                val selected = remember(tick) { p.mode } == mode
                Row(
                    Modifier.fillMaxWidth().selectable(selected, role = Role.RadioButton) { p.mode = mode; UpdateScheduler.apply(ctx); tick++ },
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    RadioButton(selected, null)
                    Column(Modifier.padding(start = 12.dp, top = 6.dp, bottom = 6.dp)) {
                        Text(stringResource(label), style = MaterialTheme.typography.bodyLarge)
                        Text(stringResource(hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
            if (remember(tick) { p.mode } != UpdatePrefs.OFF) SwitchRow(stringResource(R.string.update_wifi_only), remember(tick) { p.wifiOnly }) { p.wifiOnly = it; UpdateScheduler.apply(ctx); tick++ }
            SwitchRow(stringResource(R.string.update_beta), remember(tick) { p.beta }) { p.beta = it; tick++; check() }
            Text(stringResource(R.string.update_beta_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(stringResource(R.string.update_signature_note), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

/** On the NAS status: the NAS runs a newer StrataOS than this app. */
@Composable
fun NasNewerBanner(nasVersion: String) {
    val nas = AppVersion.parse(nasVersion) ?: return
    if (!Releases.isNewer(nas, Updater.installed)) return
    val open = LocalOpenOverlay.current
    Card(
        Modifier.fillMaxWidth().padding(vertical = 4.dp).clickable { open("update") },
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.tertiaryContainer),
    ) {
        Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Default.SystemUpdate, null)
            Text(stringResource(R.string.update_nas_newer, nas.toString(), BuildConfig.VERSION_NAME), Modifier.padding(start = 12.dp), style = MaterialTheme.typography.bodyMedium)
        }
    }
}
