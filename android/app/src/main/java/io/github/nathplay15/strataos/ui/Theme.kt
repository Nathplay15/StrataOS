package io.github.nathplay15.strataos.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** The StrataOS blue: vivid, used for the header gradient, the primary actions and the selected tab. */
private val Blue = Color(0xFF1F6BFF)

private val Light = lightColorScheme(
    primary = Blue,
    onPrimary = Color.White,
    primaryContainer = Color(0xFFDCE6FF),
    onPrimaryContainer = Color(0xFF001A4D),
    inversePrimary = Color(0xFFAFC6FF),
    secondary = Color(0xFF0091D5),
    onSecondary = Color.White,
    secondaryContainer = Color(0xFFD5EEFF),
    onSecondaryContainer = Color(0xFF00344F),
    tertiary = Color(0xFF7A4DFF),
    onTertiary = Color.White,
    tertiaryContainer = Color(0xFFE8DEFF),
    onTertiaryContainer = Color(0xFF250066),
    background = Color(0xFFF2F5FB),
    onBackground = Color(0xFF151A26),
    surface = Color(0xFFF2F5FB),
    onSurface = Color(0xFF151A26),
    surfaceVariant = Color(0xFFE4E9F2),
    onSurfaceVariant = Color(0xFF5A6377),
    surfaceTint = Blue,
    // Cards, dialogs, menus and the navigation bar are white on the light grey-blue page.
    surfaceContainerLowest = Color.White,
    surfaceContainerLow = Color(0xFFF8FAFE),
    surfaceContainer = Color.White,
    surfaceContainerHigh = Color.White,
    surfaceContainerHighest = Color.White,
    surfaceBright = Color.White,
    surfaceDim = Color(0xFFDDE3EE),
    outline = Color(0xFF7A849A),
    outlineVariant = Color(0xFFDCE2EC),
    error = Color(0xFFE5383B),
    onError = Color.White,
    errorContainer = Color(0xFFFFDAD7),
    onErrorContainer = Color(0xFF410004),
)

private val Dark = darkColorScheme(
    primary = Color(0xFF8DB2FF),
    onPrimary = Color(0xFF002A75),
    primaryContainer = Color(0xFF1E4FC4),
    onPrimaryContainer = Color(0xFFDCE6FF),
    inversePrimary = Blue,
    secondary = Color(0xFF7FCFFF),
    onSecondary = Color(0xFF00344F),
    secondaryContainer = Color(0xFF004C70),
    onSecondaryContainer = Color(0xFFD5EEFF),
    tertiary = Color(0xFFC6B0FF),
    onTertiary = Color(0xFF3B1A99),
    tertiaryContainer = Color(0xFF5530CC),
    onTertiaryContainer = Color(0xFFE8DEFF),
    background = Color(0xFF0E131E),
    onBackground = Color(0xFFE4E8F1),
    surface = Color(0xFF0E131E),
    onSurface = Color(0xFFE4E8F1),
    surfaceVariant = Color(0xFF2A3245),
    onSurfaceVariant = Color(0xFFAAB3C5),
    surfaceTint = Color(0xFF8DB2FF),
    surfaceContainerLowest = Color(0xFF0A0E17),
    surfaceContainerLow = Color(0xFF141A27),
    surfaceContainer = Color(0xFF171E2C),
    surfaceContainerHigh = Color(0xFF1C2433),
    surfaceContainerHighest = Color(0xFF1C2433),
    surfaceBright = Color(0xFF2A3245),
    surfaceDim = Color(0xFF0E131E),
    outline = Color(0xFF8790A4),
    outlineVariant = Color(0xFF2E3749),
    error = Color(0xFFFF8A80),
    onError = Color(0xFF5F0006),
    errorContainer = Color(0xFF8C1015),
    onErrorContainer = Color(0xFFFFDAD7),
)

/** Large, friendly corners everywhere. */
private val StrataShapes = Shapes(
    extraSmall = RoundedCornerShape(8.dp),
    small = RoundedCornerShape(12.dp),
    medium = RoundedCornerShape(18.dp),
    large = RoundedCornerShape(24.dp),
    extraLarge = RoundedCornerShape(30.dp),
)

private val StrataType = Typography().let { t ->
    t.copy(
        displaySmall = t.displaySmall.copy(fontWeight = FontWeight.Bold),
        headlineLarge = t.headlineLarge.copy(fontWeight = FontWeight.Bold),
        headlineMedium = t.headlineMedium.copy(fontWeight = FontWeight.Bold),
        headlineSmall = t.headlineSmall.copy(fontWeight = FontWeight.Bold),
        titleLarge = t.titleLarge.copy(fontWeight = FontWeight.SemiBold, fontSize = 22.sp),
        titleMedium = t.titleMedium.copy(fontWeight = FontWeight.SemiBold),
        titleSmall = t.titleSmall.copy(fontWeight = FontWeight.SemiBold),
        labelLarge = t.labelLarge.copy(fontWeight = FontWeight.SemiBold),
    )
}

/** Colours outside the Material scheme: the header gradient and the soft page gradient. */
@Immutable
class StrataColors(val dark: Boolean, val header: Brush, val page: Brush)

val LocalStrataColors = staticCompositionLocalOf {
    StrataColors(false, Brush.verticalGradient(listOf(Blue, Blue)), Brush.verticalGradient(listOf(Color.White, Color.White)))
}

/** Brand colours of the current theme. */
val strata: StrataColors @Composable get() = LocalStrataColors.current

/**
 * Material 3 with the StrataOS colours, light or dark like the system. The
 * wallpaper colours (dynamic colour) are not used: they would turn the brand
 * blue of the headers and tiles into whatever the wallpaper is.
 */
@Composable
fun StrataTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val brand = if (dark) {
        StrataColors(
            true,
            Brush.linearGradient(listOf(Color(0xFF173E9E), Color(0xFF1D56D6), Color(0xFF2B6FE8))),
            Brush.verticalGradient(listOf(Color(0xFF17213A), Color(0xFF0E131E))),
        )
    } else {
        StrataColors(
            false,
            Brush.linearGradient(listOf(Color(0xFF1456F0), Color(0xFF2C7BFF), Color(0xFF4FA3FF))),
            Brush.verticalGradient(listOf(Color(0xFFE3EDFF), Color(0xFFF2F5FB))),
        )
    }
    CompositionLocalProvider(LocalStrataColors provides brand) {
        MaterialTheme(colorScheme = if (dark) Dark else Light, shapes = StrataShapes, typography = StrataType, content = content)
    }
}

/** A colourful tile: two-stop gradient behind a white icon. */
@Immutable
data class TilePaint(val from: Color, val to: Color) {
    val brush: Brush get() = Brush.linearGradient(listOf(from, to))
}

/** The tile colours of the app grid, one per place. */
object Tiles {
    val files = TilePaint(Color(0xFFFFC23D), Color(0xFFFF9500))
    val photos = TilePaint(Color(0xFFFF7EB0), Color(0xFFFF4D6D))
    val status = TilePaint(Color(0xFF7C88FF), Color(0xFF4B4FE8))
    val storage = TilePaint(Color(0xFF2DD4BF), Color(0xFF0EA5E9))
    val backup = TilePaint(Color(0xFF4ADE80), Color(0xFF16A34A))
    val sync = TilePaint(Color(0xFF38BDF8), Color(0xFF2563EB))
    val apps = TilePaint(Color(0xFF60A5FA), Color(0xFF1D4ED8))
    val assistant = TilePaint(Color(0xFFC084FC), Color(0xFF9333EA))
    val vms = TilePaint(Color(0xFF94A3B8), Color(0xFF475569))
    val vpn = TilePaint(Color(0xFF2DD4BF), Color(0xFF0F766E))
    val users = TilePaint(Color(0xFFFDBA74), Color(0xFFF97316))
    val camera = TilePaint(Color(0xFFFB7185), Color(0xFFE11D48))
    val links = TilePaint(Color(0xFF67E8F9), Color(0xFF0891B2))
    val fleet = TilePaint(Color(0xFFA5B4FC), Color(0xFF6366F1))
    val settings = TilePaint(Color(0xFF9CA3AF), Color(0xFF4B5563))
    val web = TilePaint(Color(0xFF818CF8), Color(0xFF4338CA))
    val more = TilePaint(Color(0xFFCBD5E1), Color(0xFF94A3B8))
}
