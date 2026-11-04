package io.github.nathplay15.strataos.update

import android.content.Context

/** Where the app looks for its releases: the GitHub API of the StrataOS repository. */
object ReleaseSource {
    @Suppress("UNUSED_PARAMETER")
    fun api(ctx: Context): String = GITHUB_API
}
