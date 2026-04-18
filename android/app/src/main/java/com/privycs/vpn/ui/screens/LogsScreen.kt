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
import androidx.compose.material.icons.filled.ArrowBack
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
import androidx.compose.runtime.remember
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

    LaunchedEffect(Unit) {
        logLines.clear()

        // Main app event log (written by PrivycsLogger from connect / disconnect /
        // state transitions / errors). Tail the last 500 lines.
        val logFile = File(context.filesDir, "privycs-vpn.log")
        if (logFile.exists()) {
            val lines = logFile.readLines()
            val tail = if (lines.size > 500) lines.takeLast(500) else lines
            if (tail.isNotEmpty()) {
                logLines.add("== Privycs VPN events ==")
                logLines.addAll(tail)
            }
        }

        // strongSwan / charon log from active IPSec sessions. Lives next to
        // the main log file, written by CharonVpnService while it runs.
        val charonFile = File(context.filesDir, "charon.log")
        if (charonFile.exists()) {
            val lines = charonFile.readLines()
            val tail = if (lines.size > 500) lines.takeLast(500) else lines
            if (tail.isNotEmpty()) {
                if (logLines.isNotEmpty()) logLines.add("")
                logLines.add("== strongSwan charon log ==")
                logLines.addAll(tail)
            }
        }

        if (logLines.isEmpty()) {
            logLines.add("No log entries yet. Events will appear here after connect / disconnect.")
        }

        // Auto-scroll to bottom
        if (logLines.isNotEmpty()) {
            listState.animateScrollToItem(logLines.size - 1)
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
                        Icon(Icons.Filled.ArrowBack, contentDescription = "Back")
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
