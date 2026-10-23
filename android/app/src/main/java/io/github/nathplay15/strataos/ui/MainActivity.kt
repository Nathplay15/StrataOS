package io.github.nathplay15.strataos.ui

import android.content.Intent
import android.os.Bundle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_WEAK
import androidx.biometric.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import androidx.biometric.BiometricPrompt
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.MonitorHeart
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.data.PairLink
import io.github.nathplay15.strataos.data.parsePairLink
import io.github.nathplay15.strataos.notify.EventSync
import kotlin.concurrent.thread

class MainActivity : FragmentActivity() {
    private var signedIn by mutableStateOf(false)
    private var unlocked by mutableStateOf(false)
    private var tab by mutableStateOf("files")
    private var pairLink by mutableStateOf<PairLink?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        signedIn = Graph.prefs.server != null && Graph.prefs.vault.load() != null
        unlocked = savedInstanceState?.getBoolean("unlocked") ?: false
        handle(intent)
        setContent {
            StrataTheme {
                Surface(Modifier.fillMaxSize()) {
                    when {
                        !signedIn -> Onboarding(initialLink = pairLink, onLinkUsed = { pairLink = null }, onDone = { signedIn = true; unlocked = true })
                        Graph.prefs.biometricLock && !unlocked -> LockScreen { authenticate() }
                        else -> Home(tab, { tab = it }, onSignedOut = { signedIn = false })
                    }
                }
            }
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putBoolean("unlocked", unlocked)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handle(intent)
    }

    override fun onStop() {
        super.onStop()
        // Locked again when the app goes to the background (not on rotation).
        if (!isChangingConfigurations) unlocked = false
    }

    private fun handle(intent: Intent?) {
        intent?.getStringExtra("tab")?.let { tab = it }
        intent?.data?.toString()?.let { parsePairLink(it) }?.let { if (!signedIn) pairLink = it }
    }

    override fun onResume() {
        super.onResume()
        // Catch up with the NAS notifications missed while away (the periodic check runs every 15 minutes at best).
        if (signedIn) thread { runCatching { EventSync.poll(applicationContext) } }
        if (signedIn && Graph.prefs.biometricLock && !unlocked) authenticate()
    }

    private var prompting = false

    fun authenticate() {
        if (prompting) return
        val allowed = BIOMETRIC_STRONG or BIOMETRIC_WEAK or DEVICE_CREDENTIAL
        if (BiometricManager.from(this).canAuthenticate(allowed) != BiometricManager.BIOMETRIC_SUCCESS) {
            unlocked = true // nothing enrolled any more: do not lock the user out of their files
            return
        }
        prompting = true
        BiometricPrompt(this, ContextCompat.getMainExecutor(this), object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) { prompting = false; unlocked = true }
            override fun onAuthenticationError(code: Int, msg: CharSequence) { prompting = false }
        }).authenticate(
            BiometricPrompt.PromptInfo.Builder()
                .setTitle(getString(R.string.unlock_title))
                .setSubtitle(Graph.prefs.server?.hostname ?: "")
                .setAllowedAuthenticators(allowed)
                .build(),
        )
    }
}

@Composable
private fun LockScreen(unlock: () -> Unit) {
    LaunchedEffect(Unit) { unlock() }
    Column(Modifier.fillMaxSize().padding(32.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        Icon(Icons.Default.Lock, null, Modifier.size(56.dp), tint = MaterialTheme.colorScheme.primary)
        Text(stringResource(R.string.locked), style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(vertical = 16.dp))
        Button(onClick = unlock) { Text(stringResource(R.string.unlock)) }
    }
}

@Composable
private fun Home(tab: String, setTab: (String) -> Unit, onSignedOut: () -> Unit) {
    var filesPath by rememberSaveable { mutableStateOf("") }
    val tabs = listOf(
        Triple("files", R.string.tab_files, Icons.Default.Folder),
        Triple("backup", R.string.tab_backup, Icons.Default.Backup),
        Triple("status", R.string.tab_status, Icons.Default.MonitorHeart),
        Triple("settings", R.string.tab_settings, Icons.Default.Settings),
    )
    // The tabs draw their own top bar (status bar inset); the navigation bar takes the bottom one.
    Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), bottomBar = {
        NavigationBar {
            tabs.forEach { (id, label, icon) ->
                NavigationBarItem(selected = tab == id, onClick = { setTab(id) }, icon = { Icon(icon, null) }, label = { Text(stringResource(label)) })
            }
        }
    }) { pad ->
        Surface(Modifier.padding(pad)) {
            when (tab) {
                "backup" -> BackupScreen()
                "status" -> StatusScreen()
                "settings" -> SettingsScreen(onSignedOut)
                else -> FilesScreen(filesPath) { filesPath = it }
            }
        }
    }
}
