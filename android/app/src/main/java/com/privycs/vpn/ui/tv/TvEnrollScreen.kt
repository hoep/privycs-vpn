package com.privycs.vpn.ui.tv

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Button
import androidx.tv.material3.ExperimentalTvMaterial3Api
import androidx.tv.material3.MaterialTheme
import androidx.tv.material3.OutlinedButton
import androidx.tv.material3.Text
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.api.TvDeviceEnrollment
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * TV enrollment screen.
 *
 * Primary path = device-code flow ([TvDeviceEnrollment]): the TV shows a
 * short user_code + "privycs.com/link"; the user enters it on their phone
 * (already logged in); the TV polls until approved, then persists the
 * resulting (gatewayUrl, token) exactly like the QR / manual gateway
 * enrollment does (SettingsRepository.updateGatewayConfig). TvConnectScreen
 * then pulls configs from the gateway.
 *
 * Fallback path = manual entry of gateway URL + token. The device-code
 * gateway endpoints are not live yet (P0 in TV_PORT_PLAN.md), so the
 * manual path keeps the TV usable today and is also the escape hatch if
 * the device-code session errors/expires.
 *
 * Standard TextField uses an on-screen IME that is awkward but functional
 * on a TV remote; we keep entry to the minimum (URL + token) and steer
 * users toward the device-code path.
 */
@OptIn(ExperimentalTvMaterial3Api::class)
@Composable
fun TvEnrollScreen(
    onEnrolled: () -> Unit,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val settingsRepo = PrivycsApp.instance.settingsRepository

    // Default gateway base the device-code client talks to. The verification
    // URI / approval all happen server-side; the TV just needs a base URL to
    // POST start/poll against. privycs.com is the canonical gateway base.
    val deviceCodeGatewayBase = "https://www.privycs.com"

    var showManual by remember { mutableStateOf(false) }

    // Device-code session state.
    var userCode by remember { mutableStateOf("") }
    var verificationUri by remember { mutableStateOf("") }
    var statusLine by remember { mutableStateOf("") }
    var sessionError by remember { mutableStateOf<String?>(null) }
    // Bumped to (re)start a device-code session.
    var sessionAttempt by remember { mutableStateOf(0) }

    // Manual fallback fields.
    var manualUrl by remember { mutableStateOf("") }
    var manualToken by remember { mutableStateOf("") }
    var manualError by remember { mutableStateOf<String?>(null) }
    var manualSaving by remember { mutableStateOf(false) }

    // Device-code lifecycle: start → poll loop. Restarts when sessionAttempt
    // changes (initial mount = 0, retry button bumps it). Cancelled when the
    // screen leaves composition or we flip to the manual panel.
    LaunchedEffect(sessionAttempt, showManual) {
        if (showManual) return@LaunchedEffect
        sessionError = null
        userCode = ""
        verificationUri = ""
        statusLine = context.getString(R.string.tv_enroll_requesting_code)

        val enrollment = TvDeviceEnrollment(
            gatewayUrl = deviceCodeGatewayBase,
            client = TvDeviceEnrollment.CLIENT_ANDROID_TV,
            appVersion = com.privycs.vpn.BuildConfig.VERSION_NAME,
        )
        try {
            val start = enrollment.start()
            userCode = start.userCode
            verificationUri = start.verificationUri.ifBlank { "privycs.com/link" }
            statusLine = context.getString(R.string.tv_enroll_waiting)

            var intervalMs = (start.interval.coerceAtLeast(1)) * 1000L
            // Poll until approved / expired / cancelled.
            while (isActive) {
                delay(intervalMs)
                when (val r = enrollment.poll(start.deviceCode)) {
                    is TvDeviceEnrollment.PollResult.Pending -> {
                        statusLine = context.getString(R.string.tv_enroll_waiting)
                    }
                    is TvDeviceEnrollment.PollResult.SlowDown -> {
                        // Back off per RFC 8628: add 5s to the interval.
                        intervalMs += 5000L
                    }
                    is TvDeviceEnrollment.PollResult.Expired -> {
                        sessionError = context.getString(R.string.tv_enroll_expired)
                        statusLine = ""
                        break
                    }
                    is TvDeviceEnrollment.PollResult.Approved -> {
                        settingsRepo.updateGatewayConfig(r.gatewayUrl, r.token)
                        statusLine = context.getString(R.string.tv_enroll_linked)
                        onEnrolled()
                        break
                    }
                    is TvDeviceEnrollment.PollResult.Error -> {
                        // Transient (or endpoint-not-live). Surface it and
                        // stop the loop; the user can retry or go manual.
                        sessionError = context.getString(
                            R.string.tv_enroll_error, r.message
                        )
                        statusLine = ""
                        break
                    }
                }
            }
        } catch (e: Exception) {
            sessionError = context.getString(
                R.string.tv_enroll_error, e.message ?: ""
            )
            statusLine = ""
        } finally {
            enrollment.close()
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 56.dp, vertical = 40.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            text = stringResource(R.string.tv_enroll_title),
            style = MaterialTheme.typography.headlineMedium,
            fontWeight = FontWeight.Bold,
            color = MaterialTheme.colorScheme.onSurface,
        )
        Spacer(Modifier.height(24.dp))

        if (!showManual) {
            // ---- Device-code panel --------------------------------------
            Text(
                text = stringResource(R.string.tv_enroll_instructions, verificationUri.ifBlank { "privycs.com/link" }),
                style = MaterialTheme.typography.titleMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(28.dp))

            if (userCode.isNotBlank()) {
                Text(
                    text = userCode,
                    fontSize = 64.sp,
                    fontWeight = FontWeight.Bold,
                    color = MaterialTheme.colorScheme.primary,
                    textAlign = TextAlign.Center,
                )
                Spacer(Modifier.height(24.dp))
            }

            if (statusLine.isNotBlank()) {
                Text(
                    text = statusLine,
                    style = MaterialTheme.typography.bodyLarge,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Spacer(Modifier.height(16.dp))
            }

            sessionError?.let { err ->
                Text(
                    text = err,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.error,
                    textAlign = TextAlign.Center,
                )
                Spacer(Modifier.height(16.dp))
            }

            Row(horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                Button(onClick = { sessionAttempt++ }) {
                    Text(stringResource(R.string.tv_enroll_retry))
                }
                OutlinedButton(onClick = { showManual = true }) {
                    Text(stringResource(R.string.tv_enroll_manual))
                }
            }
        } else {
            // ---- Manual fallback panel ----------------------------------
            Text(
                text = stringResource(R.string.tv_enroll_manual_hint),
                style = MaterialTheme.typography.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(24.dp))

            // androidx.tv.material3 has no TextField; reuse the standard
            // Material3 one. Its on-screen IME is fully D-pad reachable.
            androidx.compose.material3.OutlinedTextField(
                value = manualUrl,
                onValueChange = { manualUrl = it },
                label = { androidx.compose.material3.Text(stringResource(R.string.tv_enroll_field_gateway_url)) },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth(0.7f),
            )
            Spacer(Modifier.height(16.dp))
            androidx.compose.material3.OutlinedTextField(
                value = manualToken,
                onValueChange = { manualToken = it },
                label = { androidx.compose.material3.Text(stringResource(R.string.tv_enroll_field_token)) },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                modifier = Modifier.fillMaxWidth(0.7f),
            )
            Spacer(Modifier.height(20.dp))

            manualError?.let { err ->
                Text(
                    text = err,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.error,
                )
                Spacer(Modifier.height(16.dp))
            }

            Row(horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                Button(
                    onClick = {
                        if (manualUrl.isBlank() || manualToken.isBlank()) {
                            manualError = context.getString(R.string.tv_enroll_manual_incomplete)
                            return@Button
                        }
                        manualSaving = true
                        manualError = null
                        scope.launch {
                            try {
                                settingsRepo.updateGatewayConfig(
                                    manualUrl.trim(), manualToken.trim()
                                )
                                onEnrolled()
                            } catch (e: Exception) {
                                manualError = context.getString(
                                    R.string.tv_enroll_error, e.message ?: ""
                                )
                            } finally {
                                manualSaving = false
                            }
                        }
                    },
                ) {
                    Text(
                        if (manualSaving) stringResource(R.string.tv_enroll_saving)
                        else stringResource(R.string.tv_enroll_save)
                    )
                }
                Spacer(Modifier.width(4.dp))
                OutlinedButton(onClick = {
                    showManual = false
                    sessionAttempt++
                }) {
                    Text(stringResource(R.string.tv_enroll_back_to_code))
                }
            }
        }
    }
}
