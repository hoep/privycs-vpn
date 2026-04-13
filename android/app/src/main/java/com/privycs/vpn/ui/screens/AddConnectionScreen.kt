package com.privycs.vpn.ui.screens

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.FileOpen
import androidx.compose.material.icons.filled.UploadFile
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.config.ConfigParser
import com.privycs.vpn.data.models.VpnProtocol

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AddConnectionScreen(
    onConnectionAdded: () -> Unit,
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val connectionRepo = remember { PrivycsApp.instance.connectionRepository }

    var selectedUri by remember { mutableStateOf<Uri?>(null) }
    var selectedFilename by remember { mutableStateOf("") }
    var fileContent by remember { mutableStateOf("") }
    var detectedProtocol by remember { mutableStateOf<VpnProtocol?>(null) }
    var connectionName by remember { mutableStateOf("") }
    var importError by remember { mutableStateOf<String?>(null) }
    var importSuccess by remember { mutableStateOf(false) }

    val filePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult

        selectedUri = uri
        importError = null
        importSuccess = false

        try {
            // Get filename from URI
            val cursor = context.contentResolver.query(uri, null, null, null, null)
            val nameIndex = cursor?.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME)
            cursor?.moveToFirst()
            selectedFilename = if (nameIndex != null && nameIndex >= 0) {
                cursor?.getString(nameIndex) ?: "unknown.conf"
            } else {
                uri.lastPathSegment ?: "unknown.conf"
            }
            cursor?.close()

            // Read content
            val inputStream = context.contentResolver.openInputStream(uri)
            fileContent = inputStream?.bufferedReader()?.readText() ?: ""
            inputStream?.close()

            if (fileContent.isBlank()) {
                importError = "File is empty"
                return@rememberLauncherForActivityResult
            }

            // Auto-detect protocol
            detectedProtocol = ConfigParser.detectProtocol(fileContent, selectedFilename)
            if (detectedProtocol == null) {
                importError = "Unable to detect VPN protocol. Supported: .conf (WireGuard), .ovpn (OpenVPN), .sswan/.mobileconfig (IPSec)"
                return@rememberLauncherForActivityResult
            }

            // Suggest connection name
            if (connectionName.isBlank()) {
                connectionName = ConfigParser.deriveConnectionName(selectedFilename)
            }
        } catch (e: Exception) {
            importError = "Failed to read file: ${e.message}"
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        "Add Connection",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        }
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            // File picker area
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(160.dp)
                    .clip(RoundedCornerShape(16.dp))
                    .border(
                        width = 2.dp,
                        color = if (detectedProtocol != null)
                            MaterialTheme.colorScheme.primary.copy(alpha = 0.5f)
                        else MaterialTheme.colorScheme.outline.copy(alpha = 0.5f),
                        shape = RoundedCornerShape(16.dp)
                    )
                    .background(MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.3f))
                    .clickable {
                        filePicker.launch(arrayOf("*/*"))
                    },
                contentAlignment = Alignment.Center
            ) {
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    if (detectedProtocol != null) {
                        Icon(
                            Icons.Filled.CheckCircle,
                            contentDescription = null,
                            modifier = Modifier.size(40.dp),
                            tint = MaterialTheme.colorScheme.primary
                        )
                        Spacer(modifier = Modifier.height(8.dp))
                        Text(
                            text = selectedFilename,
                            style = MaterialTheme.typography.bodyMedium,
                            fontWeight = FontWeight.Medium,
                            color = MaterialTheme.colorScheme.onSurface
                        )
                        Text(
                            text = "Detected: ${detectedProtocol!!.label}",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.primary
                        )
                    } else {
                        Icon(
                            Icons.Filled.UploadFile,
                            contentDescription = null,
                            modifier = Modifier.size(40.dp),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                        Spacer(modifier = Modifier.height(8.dp))
                        Text(
                            text = "Tap to select config file",
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                        Text(
                            text = "or drop file here",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.7f)
                        )
                    }
                }
            }

            Spacer(modifier = Modifier.height(20.dp))

            // Connection name input
            OutlinedTextField(
                value = connectionName,
                onValueChange = { connectionName = it },
                label = { Text("Connection Name") },
                placeholder = { Text("e.g. Office VPN") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
                enabled = detectedProtocol != null
            )

            Spacer(modifier = Modifier.height(16.dp))

            // Import button
            Button(
                onClick = {
                    if (detectedProtocol != null && fileContent.isNotBlank()) {
                        val protocolConfig = ConfigParser.buildProtocolConfig(fileContent, selectedFilename)
                        if (protocolConfig != null) {
                            val name = connectionName.ifBlank {
                                ConfigParser.deriveConnectionName(selectedFilename)
                            }
                            connectionRepo.addOrUpdate(null, name, protocolConfig)
                            importSuccess = true
                            importError = null
                            onConnectionAdded()
                        } else {
                            importError = "Failed to parse config file"
                        }
                    }
                },
                enabled = detectedProtocol != null && fileContent.isNotBlank() && !importSuccess,
                modifier = Modifier.fillMaxWidth(),
                colors = ButtonDefaults.buttonColors(
                    containerColor = MaterialTheme.colorScheme.primary
                )
            ) {
                Icon(
                    Icons.Filled.FileOpen,
                    contentDescription = null,
                    modifier = Modifier.size(18.dp)
                )
                Spacer(modifier = Modifier.size(8.dp))
                Text(if (importSuccess) "Imported" else "Import Config")
            }

            // Error message
            if (importError != null) {
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = importError!!,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.error,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth()
                )
            }

            Spacer(modifier = Modifier.height(24.dp))

            // Supported formats info
            Card(
                modifier = Modifier.fillMaxWidth(),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f)
                ),
                shape = RoundedCornerShape(12.dp)
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text(
                        text = "Supported Formats",
                        style = MaterialTheme.typography.labelMedium,
                        fontWeight = FontWeight.SemiBold,
                        color = MaterialTheme.colorScheme.onSurface
                    )
                    Spacer(modifier = Modifier.height(8.dp))
                    FormatInfo("WireGuard", ".conf", "WireGuard configuration files")
                    Spacer(modifier = Modifier.height(4.dp))
                    FormatInfo("OpenVPN", ".ovpn", "OpenVPN configuration files")
                    Spacer(modifier = Modifier.height(4.dp))
                    FormatInfo("IPSec", ".sswan / .mobileconfig", "strongSwan or Apple profile")
                }
            }
        }
    }
}

@Composable
private fun FormatInfo(name: String, extension: String, description: String) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            text = name,
            style = MaterialTheme.typography.bodySmall,
            fontWeight = FontWeight.Medium,
            color = MaterialTheme.colorScheme.onSurface,
            modifier = Modifier.weight(0.3f)
        )
        Text(
            text = extension,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.primary,
            modifier = Modifier.weight(0.3f)
        )
        Text(
            text = description,
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.weight(0.4f)
        )
    }
}
