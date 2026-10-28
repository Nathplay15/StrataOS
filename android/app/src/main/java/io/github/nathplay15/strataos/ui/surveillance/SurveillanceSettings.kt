package io.github.nathplay15.strataos.ui.surveillance

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.FrigateReq
import io.github.nathplay15.strataos.net.manage.NasJob
import io.github.nathplay15.strataos.net.manage.Nvr
import io.github.nathplay15.strataos.net.manage.NvrSettingsReq
import io.github.nathplay15.strataos.net.manage.frigateConfig
import io.github.nathplay15.strataos.net.manage.installComponent
import io.github.nathplay15.strataos.net.manage.installFrigate
import io.github.nathplay15.strataos.net.manage.nvr
import io.github.nathplay15.strataos.net.manage.nvrCleanup
import io.github.nathplay15.strataos.net.manage.saveFrigate
import io.github.nathplay15.strataos.net.manage.saveNvrSettings
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.JobDialog
import io.github.nathplay15.strataos.ui.adminkit.KeyValue
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import io.github.nathplay15.strataos.ui.formatBytes

/** The settings of the Surveillance app: recordings folder, alerts, retention, Frigate. */
@Composable
fun SurveillanceSettings(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.nvr() }
    var job by remember { mutableStateOf<NasJob?>(null) }
    AreaScreen(stringResource(R.string.cam_settings), onBack) { pad ->
        Refreshable(data, pad) { n ->
            ScrollColumn {
                RecorderSettings(n, data.reload) { job = it }
                FrigateSettings(n, data.reload) { job = it }
            }
        }
    }
    job?.let { j -> JobDialog(j) { job = null; data.reload() } }
}

@Composable
private fun RecorderSettings(n: Nvr, reload: () -> Unit, startJob: (NasJob) -> Unit) {
    val actions = rememberActions()
    val res = LocalResources.current
    var folder by remember(n.folder) { mutableStateOf(n.folder) }
    var alert by remember(n.alertMinutes) { mutableStateOf(if (n.alertMinutes > 0) n.alertMinutes.toString() else "5") }
    SectionCard(stringResource(R.string.cam_recorder)) {
        if (!n.installed) {
            Text(stringResource(R.string.cam_not_installed), style = MaterialTheme.typography.bodyMedium)
            Button(onClick = { actions.run { startJob(it.installComponent("nvr")) } }, enabled = !actions.busy) { Text(stringResource(R.string.cam_install)) }
        }
        val vols = n.volumes.orEmpty()
        if (vols.isNotEmpty()) {
            val opts = vols.map { (it.path.trimEnd('/') + "/surveillance") to "${it.label} (${stringResource(R.string.cam_free, formatBytes(it.free))})" }
            Choice(stringResource(R.string.cam_volume), opts + listOf(folder to folder).filter { f -> opts.none { it.first == f.first } }, folder, { folder = it })
        }
        Field(stringResource(R.string.cam_folder), folder, { folder = it }, error = n.folderError.ifEmpty { null }, help = stringResource(R.string.cam_folder_help))
        Field(stringResource(R.string.cam_alert_minutes), alert, { alert = it.filter(Char::isDigit) }, number = true, help = stringResource(R.string.cam_alert_help))
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Button(enabled = folder.isNotBlank() && !actions.busy, onClick = {
                actions.run(done = res.getString(R.string.b_saved), after = reload) {
                    it.saveNvrSettings(NvrSettingsReq(folder.trim(), alert.toIntOrNull() ?: 5, n.segmentSeconds))
                }
            }) { Text(stringResource(R.string.b_save)) }
            OutlinedButton(enabled = !actions.busy, onClick = {
                actions.run { val d = it.nvrCleanup().deleted; actions.say(res.getString(R.string.cam_cleanup_done, d)) }
            }) { Text(stringResource(R.string.cam_cleanup)) }
        }
    }
}

@OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
@Composable
private fun FrigateSettings(n: Nvr, reload: () -> Unit, startJob: (NasJob) -> Unit) {
    val f = n.frigate
    val actions = rememberActions()
    val res = LocalResources.current
    var detector by remember(f.detector) { mutableStateOf(f.detector.ifEmpty { "auto" }) }
    var objects by remember(f.objects) { mutableStateOf(f.objects.toSet()) }
    var notify by remember(f.notify) { mutableStateOf(f.notify) }
    var minutes by remember(f.notifyMinutes) { mutableStateOf(if (f.notifyMinutes > 0) f.notifyMinutes.toString() else "10") }
    var config by remember { mutableStateOf<String?>(null) }
    SectionCard(stringResource(R.string.cam_frigate)) {
        Text(stringResource(R.string.cam_frigate_help), style = MaterialTheme.typography.bodySmall)
        KeyValue(stringResource(R.string.cam_frigate_state), stringResource(when {
            !f.installed -> R.string.cam_frigate_not_installed
            f.running -> R.string.cam_frigate_running
            else -> R.string.cam_frigate_stopped
        }))
        if (f.installed) KeyValue(stringResource(R.string.cam_frigate_cameras), f.cameras.toString())
        if (f.resolved.isNotEmpty()) KeyValue(stringResource(R.string.cam_detector_used), f.resolved)
        if (f.error.isNotEmpty()) Text(f.error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
        Choice(stringResource(R.string.cam_detector), listOf(
            "auto" to stringResource(R.string.cam_detector_auto), "cpu" to "CPU", "openvino" to "OpenVINO (Intel)", "edgetpu" to "Coral Edge TPU",
        ), detector, { detector = it })
        Text(stringResource(R.string.cam_objects), style = MaterialTheme.typography.labelLarge)
        FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            f.allObjects.forEach { o ->
                FilterChip(selected = o in objects, onClick = { objects = if (o in objects) objects - o else objects + o }, label = { Text(objectName(o)) })
            }
        }
        SwitchLine(stringResource(R.string.cam_notify_events), notify) { notify = it }
        if (notify) Field(stringResource(R.string.cam_notify_minutes), minutes, { minutes = it.filter(Char::isDigit) }, number = true)
        Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Button(enabled = !actions.busy, onClick = {
                actions.run(done = res.getString(R.string.b_saved), after = reload) {
                    it.saveFrigate(FrigateReq(if (detector == "auto") "" else detector, f.allObjects.filter { o -> o in objects }, notify, minutes.toIntOrNull() ?: 10))
                }
            }) { Text(stringResource(R.string.b_save)) }
            if (!f.installed && f.docker) OutlinedButton(enabled = !actions.busy && n.folder.isNotEmpty(), onClick = { actions.run { startJob(it.installFrigate()) } }) {
                Text(stringResource(R.string.cam_frigate_install))
            }
            OutlinedButton(enabled = !actions.busy, onClick = { actions.run { config = it.frigateConfig().config } }) { Text(stringResource(R.string.cam_frigate_config)) }
        }
        if (!f.docker) Text(stringResource(R.string.cam_frigate_docker), style = MaterialTheme.typography.bodySmall)
    }
    config?.let { cfg ->
        AlertDialog(onDismissRequest = { config = null }, title = { Text(stringResource(R.string.cam_frigate_config)) },
            text = {
                Text(cfg, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).horizontalScroll(rememberScrollState()))
            },
            confirmButton = { TextButton(onClick = { config = null }) { Text(stringResource(R.string.b_ok)) } })
    }
}
