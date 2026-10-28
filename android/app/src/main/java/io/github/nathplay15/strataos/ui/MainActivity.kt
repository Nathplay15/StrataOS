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
import androidx.compose.material.icons.filled.AccountCircle
import androidx.compose.material.icons.filled.Dashboard
import androidx.compose.material.icons.filled.PhotoLibrary
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
import io.github.nathplay15.strataos.ui.photos.PhotosScreen
import io.github.nathplay15.strataos.ui.files.FilesScreen
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.runtime.remember
import androidx.compose.material3.NavigationRailItem
import androidx.compose.material3.NavigationRail
import androidx.compose.material3.IconButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.DropdownMenu
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Box
import androidx.activity.compose.BackHandler
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
    private var overlay by mutableStateOf<String?>(null)
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
                        else -> Home(tab, { tab = it }, overlay, { overlay = it }, onSignedOut = { signedIn = false; overlay = null })
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
        intent?.getStringExtra("tab")?.let { destinationOf(it).let { (t, o) -> tab = t; overlay = o } }
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

/** Where a tab name of a notification or an older version leads: (tab, overlay). */
fun destinationOf(tab: String): Pair<String, String?> = when (tab) {
    "photos" -> "photos" to null
    "manage", "status" -> "manage" to null
    "backup" -> "files" to "backup"
    "settings" -> "files" to "settings"
    else -> "files" to null
}

/** The account button of the top bars: who is signed in, photo backup, app settings. */
@Composable
private fun AccountButton(openOverlay: (String) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    val cfg = Graph.prefs.server
    Box {
        IconButton(onClick = { menu = true }) { Icon(Icons.Default.AccountCircle, stringResource(R.string.a_account)) }
        DropdownMenu(menu, { menu = false }) {
            Text(
                stringResource(R.string.a_account_line, cfg?.fullName?.ifEmpty { null } ?: cfg?.username ?: "", cfg?.hostname ?: ""),
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
            )
            HorizontalDivider()
            DropdownMenuItem({ Text(stringResource(R.string.tab_backup)) }, { menu = false; openOverlay("backup") }, leadingIcon = { Icon(Icons.Default.Backup, null) })
            DropdownMenuItem({ Text(stringResource(R.string.a_app_settings)) }, { menu = false; openOverlay("settings") }, leadingIcon = { Icon(Icons.Default.Settings, null) })
        }
    }
}

@Composable
private fun Home(tab: String, setTab: (String) -> Unit, overlay: String?, setOverlay: (String?) -> Unit, onSignedOut: () -> Unit) {
    var filesPath by rememberSaveable { mutableStateOf("") }
    if (overlay != null) {
        BackHandler { setOverlay(null) }
        Surface(Modifier.fillMaxSize()) {
            when (overlay) {
                "backup" -> BackupScreen(onBack = { setOverlay(null) })
                else -> SettingsScreen(onSignedOut, onBack = { setOverlay(null) })
            }
        }
        return
    }
    val tabs = listOf(
        Triple("files", R.string.tab_files, Icons.Default.Folder),
        Triple("photos", R.string.a_photos, Icons.Default.PhotoLibrary),
        Triple("manage", R.string.a_manage, Icons.Default.Dashboard),
    )
    val account: @Composable () -> Unit = { AccountButton { setOverlay(it) } }
    val content: @Composable () -> Unit = {
        androidx.compose.runtime.CompositionLocalProvider(LocalOpenInFiles provides { p -> filesPath = p; setTab("files") }) {
            when (tab) {
                "photos" -> PhotosScreen(account)
                "manage" -> ManageScreen(account)
                else -> FilesScreen(filesPath, { filesPath = it }, account)
            }
        }
    }
    if (LocalConfiguration.current.screenWidthDp >= WIDE_DP) {
        // Tablets: a navigation rail beside the content.
        Row(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.navigationBars)) {
            NavigationRail(Modifier.windowInsetsPadding(WindowInsets.statusBars)) {
                tabs.forEach { (id, label, icon) ->
                    NavigationRailItem(selected = tab == id, onClick = { setTab(id) }, icon = { Icon(icon, null) }, label = { Text(stringResource(label)) })
                }
            }
            Surface(Modifier.weight(1f).fillMaxHeight()) { content() }
        }
        return
    }
    // The tabs draw their own top bar (status bar inset); the navigation bar takes the bottom one.
    Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), bottomBar = {
        NavigationBar {
            tabs.forEach { (id, label, icon) ->
                NavigationBarItem(selected = tab == id, onClick = { setTab(id) }, icon = { Icon(icon, null) }, label = { Text(stringResource(label)) })
            }
        }
    }) { pad ->
        Surface(Modifier.padding(pad)) { content() }
    }
}
