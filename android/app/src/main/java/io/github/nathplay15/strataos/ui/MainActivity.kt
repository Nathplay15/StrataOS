package io.github.nathplay15.strataos.ui

import android.content.Intent
import android.os.Bundle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
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
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.PhotoLibrary
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
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
import androidx.compose.material3.HorizontalDivider
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.Row
import androidx.activity.compose.BackHandler
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.foundation.layout.Box
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
    private var tab by mutableStateOf("home")
    private var overlay by mutableStateOf<String?>(null)
    private var section by mutableStateOf<String?>(null)
    private var pairLink by mutableStateOf<PairLink?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        signedIn = Graph.prefs.server != null && Graph.prefs.vault.load() != null
        unlocked = savedInstanceState?.getBoolean("unlocked") ?: false
        handle(intent)
        // Killed in the background: back on the screen the user was on.
        savedInstanceState?.let { st ->
            st.getString("tab")?.let { tab = it }
            overlay = st.getString("overlay")
            section = st.getString("section")
        }
        setContent {
            StrataTheme {
                Surface(Modifier.fillMaxSize()) {
                    when {
                        // A notification of the app updates also works signed out (the check does not need the NAS).
                        !signedIn && overlay == "update" -> Surface(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.navigationBars)) {
                            BackHandler { overlay = null }
                            UpdateScreen(onBack = { overlay = null })
                        }
                        !signedIn -> Onboarding(initialLink = pairLink, onLinkUsed = { pairLink = null }, onDone = { signedIn = true; unlocked = true })
                        // The app stays composed under the lock screen, so unlocking returns
                        // to the screen the user was on (a film, a folder…), not to Home.
                        else -> Box(Modifier.fillMaxSize()) {
                            Home(tab, { tab = it; section = null }, overlay, { overlay = it }, onSignedOut = { signedIn = false; overlay = null }, section)
                            // unlocked first: the setting is not observable state, and read alone
                            // it would leave unlocked unobserved, so the lock screen would not come
                            // back after the lock was turned on until something else recomposes.
                            if (!unlocked && Graph.prefs.biometricLock) {
                                Surface(
                                    Modifier.fillMaxSize().pointerInput(Unit) {
                                        // Nothing under the lock screen can be touched.
                                        awaitPointerEventScope { while (true) awaitPointerEvent().changes.forEach { it.consume() } }
                                    },
                                ) {
                                    BackHandler { moveTaskToBack(true) }
                                    LockScreen { authenticate() }
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putBoolean("unlocked", unlocked)
        outState.putString("tab", tab)
        outState.putString("overlay", overlay)
        outState.putString("section", section)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handle(intent)
    }

    override fun onStart() {
        super.onStart()
        io.github.nathplay15.strataos.update.Updater.visible = true
        if (signedIn) io.github.nathplay15.strataos.sync.SyncLive.start(this)
        // The storage widget shows stale data when the NAS was off: refresh it.
        io.github.nathplay15.strataos.widget.StorageWidget.refresh(this)
    }

    override fun onStop() {
        super.onStop()
        io.github.nathplay15.strataos.update.Updater.visible = false
        io.github.nathplay15.strataos.sync.SyncLive.stop()
        // Locked again when the app goes to the background (not on rotation).
        if (!isChangingConfigurations) unlocked = false
        // A prompt left open is dismissed by the system without always calling
        // back: forget it, or the next one would never be shown.
        prompt?.cancelAuthentication()
        prompt = null
        prompting = false
    }

    private fun handle(intent: Intent?) {
        intent?.getStringExtra("tab")?.let { destinationOf(it).let { (t, o) -> tab = t; overlay = o } }
        section = intent?.getStringExtra("section")
        intent?.data?.toString()?.let { parsePairLink(it) }?.let { if (!signedIn) pairLink = it }
    }

    override fun onResume() {
        super.onResume()
        // Catch up with the NAS notifications missed while away (the periodic check runs every 15 minutes at best).
        if (signedIn) thread { runCatching { EventSync.poll(applicationContext) } }
        // Posted: the prompt needs the activity fully resumed to show.
        if (signedIn && Graph.prefs.biometricLock && !unlocked) window.decorView.post { authenticate() }
    }

    private var prompting = false
    private var prompt: BiometricPrompt? = null

    fun authenticate() {
        if (prompting || unlocked || isFinishing || !lifecycle.currentState.isAtLeast(androidx.lifecycle.Lifecycle.State.RESUMED)) return
        val allowed = BIOMETRIC_STRONG or BIOMETRIC_WEAK or DEVICE_CREDENTIAL
        if (BiometricManager.from(this).canAuthenticate(allowed) != BiometricManager.BIOMETRIC_SUCCESS) {
            unlocked = true // nothing enrolled any more: do not lock the user out of their files
            return
        }
        prompting = true
        prompt = BiometricPrompt(this, ContextCompat.getMainExecutor(this), object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) { prompting = false; prompt = null; unlocked = true }
            override fun onAuthenticationError(code: Int, msg: CharSequence) { prompting = false; prompt = null }
        })
        prompt!!.authenticate(
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
    "home", "status" -> "home" to null
    "assistant" -> "assistant" to null
    "manage" -> "manage" to null
    "files" -> "files" to null
    "me" -> "me" to null
    "backup" -> "me" to "backup"
    "sync" -> "me" to "sync"
    "settings" -> "me" to "settings"
    "update" -> "me" to "update"
    else -> "home" to null
}

/** The tab of the navigation bar a tab belongs to: Manage and Assistant open from Home. */
fun navTabOf(tab: String): String = when (tab) {
    "manage", "assistant" -> "home"
    else -> tab
}

@Composable
private fun Home(tab: String, setTab: (String) -> Unit, overlay: String?, setOverlay: (String?) -> Unit, onSignedOut: () -> Unit, section: String?) {
    var filesPath by rememberSaveable { mutableStateOf("") }
    var filesSearch by remember { mutableStateOf(false) }
    // A section opened from the Home grid (Storage, Status…): Manage opens on it.
    var openSection by rememberSaveable { mutableStateOf(section) }
    LaunchedEffect(section) { if (section != null) openSection = section }
    if (overlay != null) {
        BackHandler { setOverlay(null) }
        // The overlays draw their own top bar (status bar inset); keep their content above the navigation bar.
        Surface(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.navigationBars)) {
            when (overlay) {
                "backup" -> BackupScreen(onBack = { setOverlay(null) })
                "sync" -> io.github.nathplay15.strataos.ui.sync.SyncScreen(onBack = { setOverlay(null) })
                "update" -> UpdateScreen(onBack = { setOverlay(null) })
                else -> SettingsScreen(onSignedOut, onBack = { setOverlay(null) }, onOpenUpdates = { setOverlay("update") })
            }
        }
        return
    }
    val tabs = listOf(
        Triple("home", R.string.tab_home, Icons.Default.Home),
        Triple("files", R.string.tab_files, Icons.Default.Folder),
        Triple("photos", R.string.a_photos, Icons.Default.PhotoLibrary),
        Triple("me", R.string.tab_me, Icons.Default.Person),
    )
    val nav = navTabOf(tab)
    // Back from another tab goes Home first (the screens' own back handlers, registered later, come first).
    BackHandler(enabled = tab != "home") { openSection = null; setTab("home") }
    val none: @Composable () -> Unit = {}
    val go = HomeNav(
        tab = { openSection = null; setTab(it) },
        overlay = { setOverlay(it) },
        section = { id -> openSection = id; setTab("manage") },
        search = { filesPath = ""; filesSearch = true; setTab("files") },
    )
    val content: @Composable () -> Unit = {
        androidx.compose.runtime.CompositionLocalProvider(LocalOpenInFiles provides { p -> filesPath = p; setTab("files") }, LocalOpenOverlay provides setOverlay) {
            // Smooth fade between tabs instead of an abrupt swap.
            Crossfade(targetState = tab, animationSpec = tween(220), label = "tab") { t ->
                when (t) {
                    "photos" -> PhotosScreen(none)
                    "assistant" -> io.github.nathplay15.strataos.ui.assistant.AssistantScreen(none)
                    "manage" -> ManageScreen(none, initialSection = openSection, closeTo = if (openSection != null) ({ go.tab("home") }) else null, onBack = { go.tab("home") })
                    "me" -> MeScreen(go, onSignedOut)
                    "home" -> HomeScreen(go)
                    else -> FilesScreen(filesPath, { filesPath = it }, none, startSearch = filesSearch, onSearchStarted = { filesSearch = false })
                }
            }
        }
    }
    if (LocalConfiguration.current.screenWidthDp >= WIDE_DP) {
        // Tablets: a navigation rail beside the content.
        Row(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.navigationBars)) {
            NavigationRail(Modifier.windowInsetsPadding(WindowInsets.statusBars)) {
                tabs.forEach { (id, label, icon) ->
                    NavigationRailItem(selected = nav == id, onClick = { go.tab(id) }, icon = { Icon(icon, null) }, label = { Text(stringResource(label)) })
                }
            }
            Surface(Modifier.weight(1f).fillMaxHeight()) { content() }
        }
        return
    }
    // The tabs draw their own top bar (status bar inset); the navigation bar takes the bottom one.
    Scaffold(contentWindowInsets = WindowInsets(0, 0, 0, 0), bottomBar = {
        Column {
            HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.6f))
            NavigationBar(containerColor = MaterialTheme.colorScheme.surfaceContainer, tonalElevation = 0.dp) {
                tabs.forEach { (id, label, icon) ->
                    NavigationBarItem(
                        selected = nav == id, onClick = { go.tab(id) }, icon = { Icon(icon, null) }, label = { Text(stringResource(label)) },
                        colors = NavigationBarItemDefaults.colors(
                            selectedIconColor = MaterialTheme.colorScheme.primary, selectedTextColor = MaterialTheme.colorScheme.primary,
                            indicatorColor = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.7f),
                        ),
                    )
                }
            }
        }
    }) { pad ->
        Surface(Modifier.padding(pad)) { content() }
    }
}

/** Where the Home and Me screens can lead: a tab, a full screen page, a management area, the file search. */
class HomeNav(val tab: (String) -> Unit, val overlay: (String) -> Unit, val section: (String) -> Unit, val search: () -> Unit)
