package com.privycs.vpn.util

import android.app.Activity
import android.content.Context
import android.util.Log
import com.google.android.gms.common.moduleinstall.ModuleInstall
import com.google.android.gms.common.moduleinstall.ModuleInstallRequest
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScanner
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Thin wrapper around Google Play Services Code Scanner so screens can
 * invoke QR scanning with a single `scan(activity)` coroutine call and
 * receive the decoded payload as a String.
 *
 * GMS Code Scanner deliberately does NOT require the CAMERA permission
 * from the host app - Google Play Services opens its own camera UI in
 * a separate process and returns the decoded barcode via an Activity
 * Result. This avoids the permission-prompt UX friction that ML Kit
 * Barcode Scanning or ZXing would introduce, and keeps the binary
 * smaller than bundling a camera pipeline.
 *
 * The scanner restricts itself to QR codes because Privycs only deals
 * with full-config (WireGuard) or enrollment-URL (Privycs scheme) QRs,
 * never 1D barcodes.
 */
object QrCodeScanner {
    private const val TAG = "QrCodeScanner"

    /**
     * Launch the GMS code scanner UI and suspend until the user either
     * scans a code or cancels. Returns the decoded string, or null if
     * the user cancelled or the scanner failed.
     *
     * @param activity the current Activity context. Must be an Activity
     *   (not just Context) because GMS uses Activity-result APIs to
     *   return the scanned value. Passing a non-Activity context will
     *   throw at runtime.
     */
    suspend fun scan(activity: Activity): String? {
        // Ensure the scanner module is downloaded. First use pulls a
        // ~3MB Google Play module; subsequent use is instant. We fire
        // the install request but do not block on it - if the module
        // is already present, startScanIntent will succeed immediately.
        ensureModuleInstalled(activity.applicationContext)

        val options = GmsBarcodeScannerOptions.Builder()
            .setBarcodeFormats(Barcode.FORMAT_QR_CODE)
            .enableAutoZoom()
            .build()
        val scanner: GmsBarcodeScanner = GmsBarcodeScanning.getClient(activity, options)

        return suspendCancellableCoroutine { cont ->
            scanner.startScan()
                .addOnSuccessListener { barcode ->
                    val raw = barcode.rawValue
                    if (raw.isNullOrBlank()) {
                        Log.w(TAG, "scan returned empty raw value")
                        cont.resume(null)
                    } else {
                        cont.resume(raw)
                    }
                }
                .addOnCanceledListener {
                    // User closed the scanner UI without a result.
                    // Not an error condition.
                    cont.resume(null)
                }
                .addOnFailureListener { e ->
                    Log.w(TAG, "scan failed: ${e.message}")
                    cont.resumeWithException(e)
                }
        }
    }

    private fun ensureModuleInstalled(context: Context) {
        try {
            val scanner = GmsBarcodeScanning.getClient(context)
            val request = ModuleInstallRequest.newBuilder()
                .addApi(scanner)
                .build()
            ModuleInstall.getClient(context).installModules(request)
        } catch (e: Exception) {
            // Module-install failures are recoverable - the scanner
            // will still work on devices that ship with the module
            // pre-installed. Just log and move on.
            Log.w(TAG, "Module install request failed: ${e.message}")
        }
    }
}
