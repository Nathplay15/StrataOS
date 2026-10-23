package io.github.nathplay15.strataos.ui

import android.Manifest
import android.os.Build
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.biometric.BiometricManager
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.BuildConfig
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.backup.BackupScheduler
import io.github.nathplay15.strataos.backup.BackupSettings
import io.github.nathplay15.strataos.notify.EventsWorker
import io.github.nathplay15.strataos.notify.Push
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(onSignedOut: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val p = Graph.prefs
    var tick by remember { mutableIntStateOf(0) }
    var confirmSignOut by remember { mutableStateOf(false) }
    val cfg = remember(tick) { p.server }
    val askNotif = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { tick++ }
    val canBio = BiometricManager.from(ctx).canAuthenticate(
        BiometricManager.Authenticators.BIOMETRIC_STRONG or BiometricManager.Authenticators.BIOMETRIC_WEAK or BiometricManager.Authenticators.DEVICE_CREDENTIAL,
    ) == BiometricManager.BIOMETRIC_SUCCESS
    val distributors = remember(tick) { Push.distributors(ctx) }

    Column(Modifier.fillMaxSize()) {
        TopAppBar(title = { Text(stringResource(R.string.tab_settings)) })
        Column(Modifier.verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Card(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(cfg?.hostname ?: "", style = MaterialTheme.typography.titleMedium)
                    Text(cfg?.url ?: "", style = MaterialTheme.typography.bodyMedium)
                    Text(stringResource(R.string.signed_in_as, cfg?.fullName?.ifEmpty { null } ?: cfg?.username ?: "", cfg?.deviceName ?: ""), style = MaterialTheme.typography.bodySmall)
                    if (!cfg?.fingerprint.isNullOrEmpty()) {
                        Text(stringResource(R.string.trust_fingerprint), style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 8.dp))
                        Text(cfg.fingerprint, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
            Text(stringResource(R.string.security), style = MaterialTheme.typography.titleMedium)
            SwitchRow(stringResource(R.string.biometric_lock), p.biometricLock && canBio) { p.biometricLock = it; tick++ }
            if (!canBio) Text(stringResource(R.string.biometric_unavailable), style = MaterialTheme.typography.bodySmall)
            SwitchRow(stringResource(R.string.ca_only), cfg?.caOnly == true) { on ->
                cfg?.let { p.server = it.copy(caOnly = on); Graph.reset(); tick++ }
            }
            Text(stringResource(R.string.ca_only_hint), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            HorizontalDivider()
            Text(stringResource(R.string.notifications), style = MaterialTheme.typography.titleMedium)
            if (Build.VERSION.SDK_INT >= 33 && !io.github.nathplay15.strataos.notify.Notifier.canPost(ctx)) {
                Button(onClick = { askNotif.launch(Manifest.permission.POST_NOTIFICATIONS) }) { Text(stringResource(R.string.allow_notifications)) }
            }
            Text(
                when {
                    cfg?.isAdmin != true -> stringResource(R.string.push_admin_only)
                    p.pushEndpoint.isNotEmpty() -> stringResource(R.string.push_active)
                    distributors.isEmpty() -> stringResource(R.string.push_none)
                    else -> stringResource(R.string.push_available)
                },
                style = MaterialTheme.typography.bodySmall,
            )
            if (cfg?.isAdmin == true && distributors.isNotEmpty()) OutlinedButton(onClick = {
                scope.launch {
                    val vapid = withContext(Dispatchers.IO) { runCatching { Graph.api()?.info()?.vapidKey }.getOrNull() } ?: ""
                    Push.register(ctx as android.app.Activity, vapid) { ok -> if (!ok) Toast.makeText(ctx, ctx.getString(R.string.push_failed), Toast.LENGTH_LONG).show(); tick++ }
                }
            }) { Text(stringResource(R.string.push_setup)) }
            HorizontalDivider()
            Button(onClick = { confirmSignOut = true }, colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error)) { Text(stringResource(R.string.sign_out)) }
            Text(stringResource(R.string.about, BuildConfig.VERSION_NAME), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }

    if (confirmSignOut) AlertDialog(
        onDismissRequest = { confirmSignOut = false },
        title = { Text(stringResource(R.string.sign_out)) },
        text = { Text(stringResource(R.string.sign_out_text)) },
        confirmButton = {
            TextButton(onClick = {
                confirmSignOut = false
                scope.launch {
                    withContext(Dispatchers.IO) { runCatching { Graph.api()?.signOut() } }
                    Push.unregister(ctx)
                    BackupSettings(ctx).enabled = false
                    BackupScheduler.cancelAll(ctx)
                    EventsWorker.cancel(ctx)
                    Graph.backupDb.reset()
                    p.signOut()
                    Graph.reset()
                    onSignedOut()
                }
            }) { Text(stringResource(R.string.sign_out)) }
        },
        dismissButton = { TextButton(onClick = { confirmSignOut = false }) { Text(stringResource(R.string.cancel)) } },
    )
}
