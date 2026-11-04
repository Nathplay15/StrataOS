package io.github.nathplay15.strataos.update

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.net.ConnectivityManager
import android.os.Build
import androidx.core.content.edit
import androidx.core.content.pm.PackageInfoCompat
import io.github.nathplay15.strataos.BuildConfig
import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import okhttp3.OkHttpClient
import okhttp3.Request

const val GITHUB_API = "https://api.github.com/repos/Nathplay15/StrataOS"

/** Settings of the app updates (kept when signing out: they are about the app, not the NAS). */
class UpdatePrefs(ctx: Context) {
    private val p: SharedPreferences = ctx.getSharedPreferences("updates", Context.MODE_PRIVATE)

    var mode: String
        get() = p.getString("mode", NOTIFY) ?: NOTIFY
        set(v) = p.edit { putString("mode", v) }

    /** Background checks and downloads only on unmetered networks. */
    var wifiOnly: Boolean
        get() = p.getBoolean("wifiOnly", true)
        set(v) = p.edit { putBoolean("wifiOnly", v) }

    var beta: Boolean
        get() = p.getBoolean("beta", false)
        set(v) = p.edit { putBoolean("beta", v) }

    /** The newest release seen at the last check (JSON). */
    var latest: Release?
        get() = p.getString("latest", null)?.let { runCatching { json.decodeFromString(Release.serializer(), it) }.getOrNull() }
        set(v) = p.edit { if (v == null) remove("latest") else putString("latest", json.encodeToString(Release.serializer(), v)) }

    var lastCheck: Long
        get() = p.getLong("lastCheck", 0)
        set(v) = p.edit { putLong("lastCheck", v) }

    /** The release already announced by a notification (once per version). */
    var notified: String
        get() = p.getString("notified", "") ?: ""
        set(v) = p.edit { putString("notified", v) }

    /** The downloaded and verified APK waiting to be installed: tag and SHA-256. */
    var ready: Pair<String, String>?
        get() = p.getString("readyTag", null)?.let { it to (p.getString("readyHash", "") ?: "") }
        set(v) = p.edit { if (v == null) { remove("readyTag"); remove("readyHash") } else { putString("readyTag", v.first); putString("readyHash", v.second) } }

    /** Version being installed, to say afterwards that it was (the app closes meanwhile, without a word when no prompt was shown). */
    var installing: String
        get() = p.getString("installing", "") ?: ""
        set(v) = p.edit { putString("installing", v) }

    companion object {
        const val OFF = "off"
        const val NOTIFY = "notify"
        const val AUTO = "auto"
        private val json = Json { ignoreUnknownKeys = true }
    }
}

class UpdateException(val kind: Kind, val detail: String = "", cause: Throwable? = null) : Exception(detail.ifEmpty { kind.name }, cause) {
    enum class Kind { NO_APK, NO_SUMS, CHECKSUM, SIGNATURE, PACKAGE, NOT_NEWER, NOT_ALLOWED, INSTALL, CANCELLED, NETWORK }
}

/**
 * Updates of the app itself, which is sideloaded: the latest GitHub release,
 * its APK checked against the SHA256SUMS of the same release and against the
 * signing certificate of the installed app, installed with PackageInstaller.
 */
object Updater {
    enum class Step { IDLE, CHECKING, DOWNLOADING, VERIFYING, INSTALLING, WAITING_USER, FAILED }
    data class State(val step: Step = Step.IDLE, val progress: Float = -1f, val error: UpdateException? = null)

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state
    private val lock = Mutex()

    /** An activity of the app is visible: the install confirmation can be shown right away. */
    @Volatile var visible = false

    private val json = Json { ignoreUnknownKeys = true }
    private val http by lazy {
        OkHttpClient.Builder().connectTimeout(20, TimeUnit.SECONDS).readTimeout(60, TimeUnit.SECONDS).build()
    }

    fun prefs(ctx: Context) = UpdatePrefs(ctx.applicationContext)

    val installed: AppVersion get() = AppVersion.parse(BuildConfig.VERSION_NAME) ?: AppVersion.fromVersionCode(BuildConfig.VERSION_CODE.toLong())

    /** The last release seen, when it is newer than the app and has an APK. */
    fun available(ctx: Context): Release? = prefs(ctx).latest?.takeIf { r ->
        r.version?.let { Releases.isNewer(it, installed) } == true && Releases.files(r) != null
    }

    fun setError(e: UpdateException?) {
        _state.value = if (e == null) State() else State(Step.FAILED, error = e)
    }

    private fun get(url: String): okhttp3.Response {
        val req = Request.Builder().url(url)
            .header("Accept", "application/vnd.github+json")
            .header("User-Agent", "StrataOS-Android/${BuildConfig.VERSION_NAME}")
            .build()
        val res = http.newCall(req).execute()
        if (!res.isSuccessful) {
            res.close()
            throw UpdateException(UpdateException.Kind.NETWORK, "HTTP ${res.code}")
        }
        return res
    }

    /** Asks GitHub for the newest release and remembers it; null when there is none. */
    fun check(ctx: Context): Release? {
        val p = prefs(ctx)
        val api = ReleaseSource.api(ctx)
        val list = try {
            if (p.beta) {
                get("$api/releases?per_page=20").use { json.decodeFromString(ListSerializer(Release.serializer()), it.body.string()) }
            } else {
                // releases/latest never returns drafts or pre-releases.
                get("$api/releases/latest").use { listOf(json.decodeFromString(Release.serializer(), it.body.string())) }
            }
        } catch (e: IOException) {
            throw UpdateException(UpdateException.Kind.NETWORK, e.message ?: e.javaClass.simpleName, e)
        }
        val r = Releases.pick(list, p.beta)
        // Remembered even when it has no APK: the screen says so.
        p.latest = r?.copy(assets = r.assets.filter { it.name.endsWith(".apk") || it.name == Releases.SUMS })
        p.lastCheck = System.currentTimeMillis()
        return r
    }

    /** Checks with the progress shown on the update screen. */
    suspend fun checkNow(ctx: Context): Release? = lock.withLock {
        _state.value = State(Step.CHECKING)
        try {
            check(ctx).also { _state.value = State() }
        } catch (e: UpdateException) {
            setError(e)
            null
        }
    }

    private fun dir(ctx: Context) = File(ctx.cacheDir, "update").apply { mkdirs() }

    private fun apkFile(ctx: Context, r: Release) = File(dir(ctx), "strataos-${r.version!!.fileVersion}.apk")

    /** The APK of [r], already downloaded and verified, or null. */
    fun readyApk(ctx: Context, r: Release): File? {
        val (tag, hash) = prefs(ctx).ready ?: return null
        val f = apkFile(ctx, r)
        return f.takeIf { tag == r.tag && it.isFile && hash.isNotEmpty() }
    }

    /** Downloads the APK of [r], checks it against SHA256SUMS and the installed app's signature. */
    fun download(ctx: Context, r: Release, progress: (Float) -> Unit = {}): File {
        val files = Releases.files(r) ?: throw UpdateException(UpdateException.Kind.NO_APK)
        readyApk(ctx, r)?.let { return it }
        val sums = try {
            get(files.sums.url).use { Releases.parseSums(it.body.string()) }
        } catch (e: IOException) {
            throw UpdateException(UpdateException.Kind.NETWORK, e.message ?: "", e)
        }
        val expected = sums[files.apk.name] ?: throw UpdateException(UpdateException.Kind.NO_SUMS)
        dir(ctx).listFiles()?.forEach { it.delete() } // older downloads
        prefs(ctx).ready = null
        val part = File(dir(ctx), files.apk.name + ".part")
        val md = MessageDigest.getInstance("SHA-256")
        try {
            get(files.apk.url).use { res ->
                val total = res.body.contentLength().takeIf { it > 0 } ?: files.apk.size
                res.body.byteStream().use { input ->
                    part.outputStream().use { out ->
                        val buf = ByteArray(64 * 1024)
                        var done = 0L
                        while (true) {
                            val n = input.read(buf)
                            if (n < 0) break
                            out.write(buf, 0, n)
                            md.update(buf, 0, n)
                            done += n
                            if (total > 0) progress((done.toFloat() / total).coerceAtMost(1f))
                        }
                    }
                }
            }
        } catch (e: IOException) {
            part.delete()
            throw UpdateException(UpdateException.Kind.NETWORK, e.message ?: "", e)
        }
        val got = md.digest().toHex()
        if (got != expected) {
            part.delete()
            throw UpdateException(UpdateException.Kind.CHECKSUM, "expected $expected, got $got")
        }
        val apk = apkFile(ctx, r)
        if (!part.renameTo(apk)) throw UpdateException(UpdateException.Kind.INSTALL, "cannot keep the download")
        try {
            checkArchive(ctx, apk)
        } catch (e: UpdateException) {
            apk.delete()
            throw e
        }
        prefs(ctx).ready = r.tag to got
        return apk
    }

    /** The APK is this app, signed with the same key, and not older. */
    fun checkArchive(ctx: Context, apk: File) {
        val pm = ctx.packageManager
        val info = archiveInfo(pm, apk.path) ?: throw UpdateException(UpdateException.Kind.PACKAGE)
        if (info.packageName != ctx.packageName) throw UpdateException(UpdateException.Kind.PACKAGE, info.packageName)
        if (PackageInfoCompat.getLongVersionCode(info) < BuildConfig.VERSION_CODE) throw UpdateException(UpdateException.Kind.NOT_NEWER)
        val mine = installedInfo(pm, ctx.packageName)
        if (!Releases.sameSigner(signers(mine).first, signers(info).first, signers(info).second)) throw UpdateException(UpdateException.Kind.SIGNATURE)
    }

    @Suppress("DEPRECATION")
    private fun signFlags() = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES

    @SuppressLint("PackageManagerGetSignatures") // API 26-27 only; the signatures are compared whole, not just one of them.
    private fun archiveInfo(pm: PackageManager, path: String): PackageInfo? =
        if (Build.VERSION.SDK_INT >= 33) pm.getPackageArchiveInfo(path, PackageManager.PackageInfoFlags.of(signFlags().toLong()))
        else pm.getPackageArchiveInfo(path, signFlags())

    @SuppressLint("PackageManagerGetSignatures")
    private fun installedInfo(pm: PackageManager, pkg: String): PackageInfo =
        if (Build.VERSION.SDK_INT >= 33) pm.getPackageInfo(pkg, PackageManager.PackageInfoFlags.of(signFlags().toLong()))
        else pm.getPackageInfo(pkg, signFlags())

    /** Current signing certificates (SHA-256) and the rotation history of a package. */
    @Suppress("DEPRECATION")
    private fun signers(info: PackageInfo): Pair<Set<String>, List<String>> {
        fun digest(s: Signature) = MessageDigest.getInstance("SHA-256").digest(s.toByteArray()).toHex()
        if (Build.VERSION.SDK_INT >= 28) {
            val si = info.signingInfo ?: return emptySet<String>() to emptyList()
            val current = si.apkContentsSigners.orEmpty().map(::digest).toSet()
            val history = if (si.hasMultipleSigners()) emptyList() else si.signingCertificateHistory.orEmpty().map(::digest)
            return current to history
        }
        return info.signatures.orEmpty().map(::digest).toSet() to emptyList()
    }

    /** Android lets the app install packages ("Install unknown apps" allowed for StrataOS). */
    fun canInstall(ctx: Context) = ctx.packageManager.canRequestPackageInstalls()

    fun meteredNow(ctx: Context) = ctx.getSystemService(ConnectivityManager::class.java)?.isActiveNetworkMetered ?: true

    /**
     * Hands the APK to PackageInstaller. Without a prompt when Android allows
     * it (Android 12+, the app installed the version being replaced);
     * otherwise [InstallReceiver] shows the system confirmation.
     */
    fun install(ctx: Context, r: Release) {
        val apk = readyApk(ctx, r) ?: throw UpdateException(UpdateException.Kind.INSTALL, "not downloaded")
        // Checked again: the file waited in the cache since its download.
        val hash = apk.inputStream().use { i -> MessageDigest.getInstance("SHA-256").also { md -> val b = ByteArray(64 * 1024); while (true) { val n = i.read(b); if (n < 0) break; md.update(b, 0, n) } }.digest().toHex() }
        if (hash != prefs(ctx).ready?.second) { apk.delete(); prefs(ctx).ready = null; throw UpdateException(UpdateException.Kind.CHECKSUM) }
        checkArchive(ctx, apk)
        if (!canInstall(ctx)) throw UpdateException(UpdateException.Kind.NOT_ALLOWED)
        val pi = ctx.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(ctx.packageName)
            setSize(apk.length())
            setInstallReason(PackageManager.INSTALL_REASON_USER)
            if (Build.VERSION.SDK_INT >= 31) setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
            if (Build.VERSION.SDK_INT >= 33) setPackageSource(PackageInstaller.PACKAGE_SOURCE_DOWNLOADED_FILE)
        }
        prefs(ctx).installing = r.version.toString()
        val id = pi.createSession(params)
        try {
            pi.openSession(id).use { s ->
                s.openWrite("strataos.apk", 0, apk.length()).use { out ->
                    apk.inputStream().use { it.copyTo(out) }
                    s.fsync(out)
                }
                val intent = Intent(ctx, InstallReceiver::class.java).setPackage(ctx.packageName).putExtra(InstallReceiver.TAG, r.tag)
                // Mutable: PackageInstaller adds the status to this explicit intent.
                val flags = PendingIntent.FLAG_UPDATE_CURRENT or if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0
                s.commit(PendingIntent.getBroadcast(ctx, id, intent, flags).intentSender)
            }
        } catch (e: Exception) {
            runCatching { pi.abandonSession(id) }
            throw UpdateException(UpdateException.Kind.INSTALL, e.message ?: e.javaClass.simpleName, e)
        }
    }

    /** Download (if needed), checks and installation, with the progress shown on the update screen. */
    suspend fun update(ctx: Context, r: Release) = lock.withLock {
        val app = ctx.applicationContext
        try {
            if (readyApk(app, r) == null) {
                _state.value = State(Step.DOWNLOADING, 0f)
                download(app, r) { _state.value = State(Step.DOWNLOADING, it) }
            }
            _state.value = State(Step.INSTALLING)
            install(app, r)
        } catch (e: UpdateException) {
            setError(e)
            throw e
        }
    }

    internal fun onStatus(step: Step, error: UpdateException? = null) {
        _state.value = State(step, error = error)
    }

    /** After an update: the downloaded APK is no longer needed. */
    fun cleanUp(ctx: Context) {
        File(ctx.cacheDir, "update").listFiles()?.forEach { it.delete() }
        prefs(ctx).ready = null
    }
}

private fun ByteArray.toHex() = joinToString("") { "%02x".format(it) }
