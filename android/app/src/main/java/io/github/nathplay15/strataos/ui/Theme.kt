package io.github.nathplay15.strataos.ui

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

private val Accent = Color(0xFF3D6BF2)
private val Light = lightColorScheme(primary = Accent, secondary = Color(0xFF6B5CE7), tertiary = Color(0xFF0E9F8E))
private val Dark = darkColorScheme(primary = Color(0xFF9DB4FF), secondary = Color(0xFFB9AEFF), tertiary = Color(0xFF5BD8C6))

/** Material 3, light or dark like the system, with the wallpaper colours on Android 12+. */
@Composable
fun StrataTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val scheme = when {
        Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(LocalContext.current) else dynamicLightColorScheme(LocalContext.current)
        dark -> Dark
        else -> Light
    }
    MaterialTheme(colorScheme = scheme, content = content)
}
