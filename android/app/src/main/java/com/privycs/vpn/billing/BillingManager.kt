package com.privycs.vpn.billing

import android.app.Activity
import android.content.Context
import com.android.billingclient.api.AcknowledgePurchaseParams
import com.android.billingclient.api.BillingClient
import com.android.billingclient.api.BillingClientStateListener
import com.android.billingclient.api.BillingFlowParams
import com.android.billingclient.api.BillingResult
import com.android.billingclient.api.PendingPurchasesParams
import com.android.billingclient.api.ProductDetails
import com.android.billingclient.api.Purchase
import com.android.billingclient.api.PurchasesUpdatedListener
import com.android.billingclient.api.QueryProductDetailsParams
import com.android.billingclient.api.QueryPurchasesParams
import com.privycs.vpn.data.EntitlementRepository
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Google Play Billing wrapper for the one-time "Privycs Pro" purchase
 * (managed / non-consumable product `privycs_pro_lifetime`).
 *
 * Created once at app start (main process only — see PrivycsApp). The
 * BillingClient connects asynchronously and is reconnected on
 * disconnect. Purchase results flow into [EntitlementRepository].
 * [launchPurchase] needs the foreground Activity and is called from
 * the Pro upgrade screen.
 */
class BillingManager(
    context: Context,
    private val entitlements: EntitlementRepository,
) {
    enum class State { IDLE, CONNECTING, READY, UNAVAILABLE }

    private val _state = MutableStateFlow(State.IDLE)
    val state: StateFlow<State> = _state.asStateFlow()

    /** Localised price of the Pro product once product details load. */
    private val _price = MutableStateFlow<String?>(null)
    val price: StateFlow<String?> = _price.asStateFlow()

    private var productDetails: ProductDetails? = null

    private val purchasesListener = PurchasesUpdatedListener { result, purchases ->
        when (result.responseCode) {
            BillingClient.BillingResponseCode.OK ->
                purchases?.forEach { handlePurchase(it) }
            BillingClient.BillingResponseCode.USER_CANCELED ->
                PrivycsLogger.i(TAG, "purchase cancelled by user")
            else ->
                PrivycsLogger.w(TAG, "purchase update failed: code=${result.responseCode}")
        }
    }

    private val client: BillingClient = BillingClient.newBuilder(context.applicationContext)
        .setListener(purchasesListener)
        .enablePendingPurchases(
            PendingPurchasesParams.newBuilder().enableOneTimeProducts().build(),
        )
        .build()

    /** Connect to Play Billing and restore any owned purchase. Idempotent. */
    fun start() {
        if (_state.value == State.CONNECTING || _state.value == State.READY) return
        _state.value = State.CONNECTING
        client.startConnection(object : BillingClientStateListener {
            override fun onBillingSetupFinished(result: BillingResult) {
                if (result.responseCode == BillingClient.BillingResponseCode.OK) {
                    _state.value = State.READY
                    queryProduct()
                    refreshPurchases()
                } else {
                    _state.value = State.UNAVAILABLE
                    PrivycsLogger.w(TAG, "billing setup failed: code=${result.responseCode}")
                }
            }

            override fun onBillingServiceDisconnected() {
                _state.value = State.IDLE
            }
        })
    }

    private fun queryProduct() {
        val params = QueryProductDetailsParams.newBuilder()
            .setProductList(
                listOf(
                    QueryProductDetailsParams.Product.newBuilder()
                        .setProductId(PRO_PRODUCT_ID)
                        .setProductType(BillingClient.ProductType.INAPP)
                        .build(),
                ),
            )
            .build()
        client.queryProductDetailsAsync(params) { result, details ->
            if (result.responseCode == BillingClient.BillingResponseCode.OK) {
                productDetails = details.firstOrNull()
                _price.value = productDetails?.oneTimePurchaseOfferDetails?.formattedPrice
            } else {
                PrivycsLogger.w(TAG, "queryProductDetails failed: code=${result.responseCode}")
            }
        }
    }

    /**
     * Query owned purchases — the authoritative Pro refresh. A failed
     * (offline) query never revokes; only a successful query that finds
     * no owned purchase clears a Play entitlement.
     */
    fun refreshPurchases() {
        if (_state.value != State.READY) return
        val params = QueryPurchasesParams.newBuilder()
            .setProductType(BillingClient.ProductType.INAPP)
            .build()
        client.queryPurchasesAsync(params) { result, purchases ->
            if (result.responseCode != BillingClient.BillingResponseCode.OK) {
                return@queryPurchasesAsync
            }
            val owned = purchases.any {
                it.products.contains(PRO_PRODUCT_ID) &&
                    it.purchaseState == Purchase.PurchaseState.PURCHASED
            }
            if (owned) {
                purchases.forEach { handlePurchase(it) }
            } else {
                entitlements.revokeIfPlayOnly()
            }
        }
    }

    /**
     * Launch the Play purchase UI. Must be called with the foreground
     * Activity. Returns false if billing is not ready or the product
     * details have not loaded yet.
     */
    fun launchPurchase(activity: Activity): Boolean {
        val details = productDetails
        if (_state.value != State.READY || details == null) {
            PrivycsLogger.w(TAG, "launchPurchase: not ready (state=${_state.value})")
            return false
        }
        val params = BillingFlowParams.newBuilder()
            .setProductDetailsParamsList(
                listOf(
                    BillingFlowParams.ProductDetailsParams.newBuilder()
                        .setProductDetails(details)
                        .build(),
                ),
            )
            .build()
        val result = client.launchBillingFlow(activity, params)
        return result.responseCode == BillingClient.BillingResponseCode.OK
    }

    private fun handlePurchase(purchase: Purchase) {
        if (purchase.purchaseState != Purchase.PurchaseState.PURCHASED) return
        if (!purchase.products.contains(PRO_PRODUCT_ID)) return
        // Grant immediately so the UI unlocks without waiting on the ack.
        entitlements.grantFromPlay()
        // A purchase must be acknowledged within 3 days or Google
        // auto-refunds it — do it now.
        if (!purchase.isAcknowledged) {
            val params = AcknowledgePurchaseParams.newBuilder()
                .setPurchaseToken(purchase.purchaseToken)
                .build()
            client.acknowledgePurchase(params) { result ->
                if (result.responseCode != BillingClient.BillingResponseCode.OK) {
                    PrivycsLogger.w(TAG, "acknowledge failed: code=${result.responseCode}")
                }
            }
        }
    }

    companion object {
        private const val TAG = "BillingManager"

        /** Play Console managed-product ID for the one-time Pro purchase. */
        const val PRO_PRODUCT_ID = "privycs_pro_lifetime"
    }
}
