package com.privycs.vpn.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.io.File

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun LogsScreen(
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val clipboardManager = LocalClipboardManager.current
    val logLines = remember { mutableStateListOf<String>() }
    val listState = rememberLazyListState()
    var lastSnapshotKey by remember { mutableStateOf("") }

    // Live tail. Polls the two log files every 1.5s while the screen
    // is in composition and re-renders if their (size,mtime) changed.
    // Pre-fix the screen used LaunchedEffect(Unit) which runs once on
    // entry, so any log lines written by the service during a connect
    // never made it into the visible list - the user-reported "logs
    // nicht upgedatet" bug. Polling is the simplest fix that avoids
    // pulling in FileObserver lifecycle and gives the same effect at
    // ~negligible CPU cost (two stat() calls + a conditional readLines
    // when timestamps actually move).
    LaunchedEffect(Unit) {
        val logFile = File(context.filesDir, "privycs-vpn.log")
        val charonFile = File(context.filesDir, "charon.log")
        var firstPass = true

        while (true) {
            val key = "${logFile.length()}-${logFile.lastModified()}-" +
                "${charonFile.length()}-${charonFile.lastModified()}"
            if (key != lastSnapshotKey) {
                lastSnapshotKey = key
                val newLines = mutableListOf<String>()

                // Main app event log (written by PrivycsLogger from
                // connect / disconnect / state transitions / errors).
                // Tail the last 500 lines.
                if (logFile.exists()) {
                    val lines = logFile.readLines()
                    val tail = if (lines.size > 500) lines.takeLast(500) else lines
                    if (tail.isNotEmpty()) {
                        newLines.add("== Privycs VPN events ==")
                        newLines.addAll(tail)
                    }
                }

                // strongSwan / charon log from active IPSec sessions.
                // Lives next to the main log file, written by
                // CharonVpnService while it runs.
                if (charonFile.exists()) {
                    val lines = charonFile.readLines()
                    val tail = if (lines.size > 500) lines.takeLast(500) else lines
                    if (tail.isNotEmpty()) {
                        if (newLines.isNotEmpty()) newLines.add("")
                        newLines.add("== strongSwan charon log ==")
                        newLines.addAll(tail)
                    }
                }

                if (newLines.isEmpty()) {
                    newLines.add(
                        "No log entries yet. Events will appear here after connect / disconnect."
                    )
                }

                logLines.clear()
                logLines.addAll(newLines)

                // Auto-scroll to bottom on first pass and whenever
                // the log grows. Without this each tail re-read
                // jumps the user back up.
                if (logLines.isNotEmpty()) {
                    if (firstPass) {
                        listState.scrollToItem(logLines.size - 1)
                    } else {
                        listState.animateScrollToItem(logLines.size - 1)
                    }
                }
                firstPass = false
            }
            kotlinx.coroutines.delay(1500)
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        "Logs",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    IconButton(onClick = {
                        clipboardManager.setText(AnnotatedString(logLines.joinToString("\n")))
                    }) {
                        Icon(
                            Icons.Filled.ContentCopy,
                            contentDescription = "Copy logs",
                            tint = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                    IconButton(onClick = {
                        listOf("privycs-vpn.log", "charon.log").forEach { name ->
                            val f = File(context.filesDir, name)
                            if (f.exists()) f.writeText("")
                        }
                        logLines.clear()
                        logLines.add("Logs cleared.")
                    }) {
                        Icon(
                            Icons.Filled.Delete,
                            contentDescription = "Clear logs",
                            tint = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 8.dp)
                .clip(RoundedCornerShape(8.dp))
                .background(MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.3f))
                .padding(8.dp),
            state = listState
        ) {
            items(logLines) { line ->
                val color = when {
                    line.contains("ERROR", ignoreCase = true) -> MaterialTheme.colorScheme.error
                    line.contains("WARN", ignoreCase = true) -> MaterialTheme.colorScheme.tertiary
                    line.contains("DEBUG", ignoreCase = true) -> MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.6f)
                    else -> MaterialTheme.colorScheme.onSurface.copy(alpha = 0.8f)
                }

                Text(
                    text = line,
                    style = MaterialTheme.typography.bodySmall,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 10.sp,
                    color = color,
                    modifier = Modifier
                        .fillMaxWidth()
                        .horizontalScroll(rememberScrollState())
                        .padding(vertical = 1.dp)
                )
            }

            item { Spacer(modifier = Modifier.height(16.dp)) }
        }
    }
}
