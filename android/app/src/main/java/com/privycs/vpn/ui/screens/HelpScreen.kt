package com.privycs.vpn.ui.screens

import android.graphics.Color
import android.text.method.LinkMovementMethod
import android.util.TypedValue
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import io.noties.markwon.Markwon
import io.noties.markwon.ext.tables.TablePlugin
import io.noties.markwon.html.HtmlPlugin
import io.noties.markwon.linkify.LinkifyPlugin
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.net.HttpURLConnection
import java.net.URL

/**
 * In-app Help screen — fetches the Android-specific markdown doc
 * from the public Privycs site and renders it natively via Markwon.
 *
 * Design choices:
 *  - Live fetch over bundled asset: the doc moves with each release;
 *    bundling it would mean re-shipping the APK every time we tweak
 *    a sentence. The HTTPS fetch gives us "always current" for free.
 *    Cost is one ~30 KB download on first open per session.
 *  - Markwon (TextView-backed) over a Compose-native renderer: it
 *    handles tables, code blocks, autolinking, and inline HTML out
 *    of the box. Compose-native markdown libs (compose-richtext,
 *    halilibo) have weaker table support and gloss over inline HTML
 *    that our docs use for `<kbd>` / `<sup>` etc.
 *  - One-shot fetch on screen-enter, no caching across navigations
 *    — keeps the implementation tiny and the user always sees the
 *    latest markdown the moment they re-open Help.
 */
private const val DOC_URL = "https://www.privycs.com/docs/android-client.md"

@Composable
fun HelpScreen() {
    var state by remember { mutableStateOf<HelpState>(HelpState.Loading) }

    LaunchedEffect(Unit) {
        state = HelpState.Loading
        state = try {
            val md = withContext(Dispatchers.IO) { fetchDoc(DOC_URL) }
            HelpState.Loaded(md)
        } catch (t: Throwable) {
            HelpState.Error(t.message ?: t::class.java.simpleName)
        }
    }

    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(MaterialTheme.colorScheme.background)
    ) {
        when (val s = state) {
            HelpState.Loading -> Centered { CircularProgressIndicator() }
            is HelpState.Error -> Centered {
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(
                        text = "Could not load help",
                        style = MaterialTheme.typography.titleMedium,
                        color = MaterialTheme.colorScheme.onBackground,
                    )
                    Spacer(Modifier.height(8.dp))
                    Text(
                        text = s.message,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Spacer(Modifier.height(8.dp))
                    TextButton(onClick = {
                        state = HelpState.Loading
                    }) { Text("Retry") }
                }
            }
            is HelpState.Loaded -> MarkdownView(s.markdown)
        }
    }
}

@Composable
private fun MarkdownView(markdown: String) {
    val onSurface = MaterialTheme.colorScheme.onSurface.toArgb()
    val linkColor = MaterialTheme.colorScheme.primary.toArgb()
    AndroidView(
        modifier = Modifier.fillMaxSize(),
        factory = { ctx ->
            ScrollView(ctx).apply {
                isFillViewport = true
                addView(LinearLayout(ctx).apply {
                    orientation = LinearLayout.VERTICAL
                    layoutParams = ViewGroup.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT,
                        ViewGroup.LayoutParams.WRAP_CONTENT,
                    )
                    val pad = (16 * resources.displayMetrics.density).toInt()
                    setPadding(pad, pad, pad, pad)
                    addView(TextView(ctx).apply {
                        setTextColor(onSurface)
                        setLinkTextColor(linkColor)
                        setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
                        movementMethod = LinkMovementMethod.getInstance()
                        // Tag for later update.
                        tag = "md"
                    })
                })
            }
        },
        update = { sv ->
            val container = sv.getChildAt(0) as LinearLayout
            val tv = container.findViewWithTag<TextView>("md")
                ?: container.getChildAt(0) as TextView
            val markwon = Markwon.builder(sv.context)
                .usePlugin(TablePlugin.create(sv.context))
                .usePlugin(HtmlPlugin.create())
                .usePlugin(LinkifyPlugin.create())
                .build()
            markwon.setMarkdown(tv, markdown)
        },
    )
}

@Composable
private fun Centered(content: @Composable () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) { content() }
}

private sealed interface HelpState {
    data object Loading : HelpState
    data class Loaded(val markdown: String) : HelpState
    data class Error(val message: String) : HelpState
}

private fun fetchDoc(url: String): String {
    val conn = (URL(url).openConnection() as HttpURLConnection).apply {
        connectTimeout = 10_000
        readTimeout = 15_000
        requestMethod = "GET"
        instanceFollowRedirects = true
    }
    return try {
        val code = conn.responseCode
        if (code !in 200..299) {
            error("HTTP $code")
        }
        conn.inputStream.bufferedReader(Charsets.UTF_8).use { it.readText() }
    } finally {
        conn.disconnect()
    }
}
