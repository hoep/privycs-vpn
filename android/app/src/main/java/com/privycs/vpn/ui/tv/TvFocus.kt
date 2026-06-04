package com.privycs.vpn.ui.tv

import androidx.compose.foundation.border
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.composed
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.unit.dp

/**
 * D-pad focus highlight for the TV UI.
 *
 * The TV screens use regular [androidx.compose.material3] components (not the
 * androidx.tv Compose-for-TV library, which requires AGP 8.6.0 vs the project's
 * pinned 8.3.2). material3's clickable Card / Button / OutlinedButton are
 * already focusable + D-pad-activatable on Android TV, but they render no
 * visible focus state on their own. This modifier draws a thick primary-colour
 * border around the element while it (or a child) holds focus, so the user can
 * always see which control the D-pad will activate — the one TV affordance the
 * androidx.tv library used to provide automatically.
 */
fun Modifier.tvFocusBorder(
    shape: Shape = RoundedCornerShape(12.dp),
): Modifier = composed {
    var focused by remember { mutableStateOf(false) }
    val borderColor =
        if (focused) MaterialTheme.colorScheme.primary else Color.Transparent
    this
        .onFocusChanged { focused = it.isFocused }
        .border(width = 3.dp, color = borderColor, shape = shape)
}
