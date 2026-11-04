package io.github.nathplay15.strataos.update

import android.content.Context
import java.io.File

/**
 * Debug builds only (the release source set has no override): the API base
 * can be replaced to test updates against a local copy of the release files,
 * written with `adb shell run-as io.github.nathplay15.strataos sh -c
 * 'echo http://10.0.2.2:8765 > files/update-api'` (serving releases/latest,
 * releases/index.html and the files the JSON links to).
 */
object ReleaseSource {
    fun api(ctx: Context): String =
        runCatching { File(ctx.filesDir, "update-api").readText().trim() }.getOrNull()?.takeIf { it.startsWith("http") } ?: GITHUB_API
}
