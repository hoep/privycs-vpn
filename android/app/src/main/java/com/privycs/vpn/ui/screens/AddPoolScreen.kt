package com.privycs.vpn.ui.screens

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowBack
import androidx.compose.material.icons.filled.WarningAmber
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.PoolImportProgress
import com.privycs.vpn.data.models.PoolPolicy
import kotlinx.coroutines.launch

private const val MEMBER_COUNT_WARNING = 200

/**
 * AddPoolScreen — three-step flow:
 *   1. Pick file(s) via SAF (zip or individual configs)
 *   2. Choose name + policy + (for round-robin) interval
 *   3. Import progress + result + create
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AddPoolScreen(
    onCancel: () -> Unit,
    onCreated: () -> Unit,
    importer: AddPoolImporter
) {
    var name by remember { mutableStateOf("") }
    var policy by remember { mutableStateOf(PoolPolicy.GEO_NEAREST) }
    var intervalMin by remember { mutableStateOf("30") }
    var pickedUris by remember { mutableStateOf<List<Uri>>(emptyList()) }

    val progress by importer.progress.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current

    val pickerLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenMultipleDocuments()
    ) { uris ->
        if (uris.isNotEmpty()) {
            pickedUris = uris
            // Default name from first picked file.
            if (name.isEmpty()) {
                val first = uris.first()
                name = context.getString(
                    R.string.addpool_default_name,
                    first.lastPathSegment ?: context.getString(R.string.addpool_default_name_fallback)
                )
            }
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(stringResource(R.string.addpool_title)) },
                navigationIcon = {
                    IconButton(onClick = onCancel) {
                        Icon(
                            Icons.Filled.ArrowBack,
                            contentDescription = stringResource(R.string.addpool_cancel)
                        )
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        },
        containerColor = MaterialTheme.colorScheme.background
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 16.dp, vertical = 12.dp)
                // Scrollable so the Create button stays reachable
                // even when Round-Robin policy expands the rotation-
                // interval field and the < 5min battery warning.
                // Without scroll, on small screens the button got
                // pushed off the bottom and looked "greyed out /
                // disappeared" - reported by the user as "wird
                // import wieder ausgegraut" after switching to
                // Round-Robin. The button is fine; it was just
                // invisible.
                .verticalScroll(androidx.compose.foundation.rememberScrollState())
                .imePadding()
        ) {
            // Step 1: file picker
            Card(
                modifier = Modifier.fillMaxWidth(),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surface
                )
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text(
                        stringResource(R.string.addpool_step1_title),
                        style = MaterialTheme.typography.titleSmall
                    )
                    Spacer(Modifier.height(8.dp))
                    Text(
                        if (pickedUris.isEmpty()) stringResource(R.string.addpool_step1_hint)
                        else stringResource(R.string.addpool_files_selected, pickedUris.size),
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Spacer(Modifier.height(12.dp))
                    FilledTonalButton(
                        onClick = {
                            pickerLauncher.launch(arrayOf(
                                "application/zip",
                                "application/octet-stream",
                                "*/*"
                            ))
                        },
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text(
                            if (pickedUris.isEmpty()) stringResource(R.string.addpool_choose_files)
                            else stringResource(R.string.addpool_replace_selection)
                        )
                    }
                }
            }

            Spacer(Modifier.height(12.dp))

            // Step 2: settings
            Card(
                modifier = Modifier.fillMaxWidth(),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surface
                )
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text(
                        stringResource(R.string.addpool_step2_title),
                        style = MaterialTheme.typography.titleSmall
                    )
                    Spacer(Modifier.height(8.dp))

                    OutlinedTextField(
                        value = name,
                        onValueChange = { name = it },
                        label = { Text(stringResource(R.string.addpool_pool_name_label)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth()
                    )
                    Spacer(Modifier.height(12.dp))

                    Text(
                        stringResource(R.string.addpool_policy_label),
                        style = MaterialTheme.typography.labelMedium
                    )
                    Spacer(Modifier.height(4.dp))
                    SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                        val policies = PoolPolicy.values()
                        policies.forEachIndexed { i, p ->
                            SegmentedButton(
                                selected = policy == p,
                                onClick = { policy = p },
                                shape = SegmentedButtonDefaults.itemShape(index = i, count = policies.size)
                            ) { Text(p.displayName) }
                        }
                    }

                    if (policy == PoolPolicy.ROUND_ROBIN) {
                        Spacer(Modifier.height(12.dp))
                        OutlinedTextField(
                            value = intervalMin,
                            onValueChange = {
                                // Numeric only. Empty allowed during
                                // typing; on commit we backfill 30 if
                                // still empty.
                                intervalMin = it.filter { ch -> ch.isDigit() }
                            },
                            label = { Text(stringResource(R.string.addpool_interval_label)) },
                            placeholder = { Text(stringResource(R.string.addpool_interval_placeholder)) },
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                            singleLine = true,
                            supportingText = {
                                Text(stringResource(R.string.addpool_interval_supporting))
                            },
                            modifier = Modifier.fillMaxWidth()
                        )
                        val intervalNum = intervalMin.toIntOrNull()
                        if (intervalNum != null && intervalNum < 5) {
                            Spacer(Modifier.height(8.dp))
                            BatteryWarning(stringResource(R.string.addpool_warning_short_interval))
                        }
                    }
                }
            }

            // Soft warning for big pools — only fires once we KNOW
            // the actual member count (after parse/resolve has
            // surfaced .total). Comparing against pickedUris.size
            // would mislead on ZIPs (one Uri can expand to hundreds).
            val showLargePoolWarning = progress.total >= MEMBER_COUNT_WARNING ||
                    progress.imported >= MEMBER_COUNT_WARNING
            if (showLargePoolWarning) {
                Spacer(Modifier.height(12.dp))
                BatteryWarning(
                    stringResource(R.string.addpool_warning_large_pool, MEMBER_COUNT_WARNING)
                )
            }

            // Step 3: progress + create button.
            if (progress.stage != PoolImportProgress.Stage.DONE && progress.total > 0) {
                Spacer(Modifier.height(16.dp))
                ImportProgressCard(progress)
            }

            Spacer(Modifier.height(20.dp))

            // Validation hint shown above the Create button when it
            // is disabled. Previously the button just visually faded
            // out via Material's disabled state, which several users
            // misread as "the button disappeared". An explicit hint
            // keeps the button anchored and tells the user what's
            // missing.
            val missingFiles = pickedUris.isEmpty()
            val missingName = name.isBlank()
            val isImporting = progress.stage == PoolImportProgress.Stage.RESOLVING ||
                    progress.stage == PoolImportProgress.Stage.PARSING
            val canCreate = !missingFiles && !missingName && !isImporting

            if (!canCreate && !isImporting) {
                val hint = when {
                    missingFiles && missingName -> stringResource(R.string.addpool_hint_files_and_name)
                    missingFiles -> stringResource(R.string.addpool_hint_files)
                    else -> stringResource(R.string.addpool_hint_name)
                }
                Text(
                    text = hint,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(bottom = 8.dp)
                )
            }

            Button(
                enabled = canCreate,
                onClick = {
                    val rotationInterval = intervalMin.toIntOrNull() ?: 30
                    scope.launch {
                        importer.runImport(
                            uris = pickedUris,
                            name = name,
                            policy = policy,
                            intervalMin = rotationInterval
                        )
                        onCreated()
                    }
                },
                modifier = Modifier.fillMaxWidth()
            ) {
                Text(stringResource(R.string.addpool_create_button))
            }
        }
    }
}

@Composable
private fun ImportProgressCard(progress: PoolImportProgress) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp)) {
            val label = when (progress.stage) {
                PoolImportProgress.Stage.EXTRACTING ->
                    stringResource(R.string.addpool_progress_extracting)
                PoolImportProgress.Stage.PARSING ->
                    stringResource(R.string.addpool_progress_parsing, progress.current, progress.total)
                PoolImportProgress.Stage.RESOLVING ->
                    stringResource(R.string.addpool_progress_resolving, progress.current, progress.total)
                PoolImportProgress.Stage.DONE ->
                    stringResource(R.string.addpool_progress_done, progress.imported, progress.skipped)
            }
            Text(label, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(8.dp))
            if (progress.total > 0) {
                LinearProgressIndicator(
                    progress = { progress.current.toFloat() / progress.total },
                    modifier = Modifier.fillMaxWidth()
                )
            } else {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
            }
        }
    }
}

@Composable
private fun BatteryWarning(text: String) {
    Card {
        Row(modifier = Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Filled.WarningAmber, contentDescription = null,
                tint = MaterialTheme.colorScheme.tertiary)
            Spacer(Modifier.width(8.dp))
            Text(text, style = MaterialTheme.typography.bodySmall)
        }
    }
}

/**
 * Bridge interface — the hosting Activity wires this to the
 * PoolImporter + PoolRepository. Keeps the Composable testable
 * without an Application context.
 */
interface AddPoolImporter {
    val progress: kotlinx.coroutines.flow.StateFlow<PoolImportProgress>
    suspend fun runImport(uris: List<Uri>, name: String, policy: PoolPolicy, intervalMin: Int)
}

