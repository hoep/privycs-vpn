package com.privycs.vpn.ui.screens

import android.net.Uri
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.util.proGateAllowed
import com.privycs.vpn.data.models.PoolImportProgress
import com.privycs.vpn.data.models.PoolPolicy
import com.privycs.vpn.data.models.PoolRotation
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

/**
 * Wires the pure-UI AddPoolScreen Composable to the actual
 * PoolImporter + PoolRepository singletons living on
 * PrivycsApp. Keeps AddPoolScreen testable in isolation and
 * pushes the I/O concerns into this host layer.
 */
@Composable
fun AddPoolHost(
    onCancel: () -> Unit,
    onCreated: () -> Unit
) {
    val app = PrivycsApp.instance
    val importer = app.poolImporter
    val pools = app.poolRepository
    val context = LocalContext.current

    // Local progress state — Composable observes a StateFlow
    // (single current value) rather than the importer's
    // SharedFlow (replay queue).
    val progressState = remember {
        MutableStateFlow(PoolImportProgress(stage = PoolImportProgress.Stage.DONE))
    }

    AddPoolScreen(
        onCancel = onCancel,
        onCreated = onCreated,
        importer = object : AddPoolImporter {
            override val progress: StateFlow<PoolImportProgress> = progressState.asStateFlow()

            override suspend fun runImport(
                uris: List<Uri>,
                name: String,
                policy: PoolPolicy,
                intervalMin: Int
            ) {
                // Pro gate 5 — creating a connection pool.
                if (!proGateAllowed(context)) return
                val ioScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
                // Forward importer's SharedFlow to our local
                // StateFlow so the Compose UI updates live.
                val collectorJob = ioScope.launch {
                    importer.progress.collect { progressState.value = it }
                }
                try {
                    val result = importer.importFromUris(uris)
                    if (result.members.isNotEmpty()) {
                        val pool = pools.create(name, policy, result.members)
                        if (policy == PoolPolicy.ROUND_ROBIN) {
                            pools.update(
                                pool.copy(rotation = PoolRotation(
                                    intervalMin = intervalMin.coerceAtLeast(1),
                                    idleAware = true,
                                    forceAfterMin = (intervalMin * 2).coerceAtLeast(15)
                                ))
                            )
                        }
                    }
                } finally {
                    collectorJob.cancel()
                }
            }
        }
    )
}
