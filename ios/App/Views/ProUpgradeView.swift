import SwiftUI
import StoreKit
import PrivycsCore

struct ProUpgradeView: View {
    @EnvironmentObject private var appState: AppState
    @State private var products: [Product] = []
    @State private var purchasing = false
    @State private var message: String?

    var body: some View {
        ScrollView {
            VStack(spacing: 16) {
                Image(systemName: "star.circle.fill")
                    .font(.system(size: 80))
                    .foregroundStyle(.yellow)
                Text("Privycs Pro")
                    .font(.largeTitle).bold()
                Text("Unlimited configs, pool rotation, geo-nearest routing, and more — one-time purchase, no subscription.")
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal)

                ForEach(products, id: \.id) { product in
                    Button {
                        Task { await purchase(product) }
                    } label: {
                        VStack(spacing: 4) {
                            Text(product.displayName).font(.headline)
                            Text(product.displayPrice).font(.title3)
                        }
                        .frame(maxWidth: .infinity)
                        .padding()
                        .background(RoundedRectangle(cornerRadius: 12).fill(Color.accentColor.opacity(0.15)))
                    }
                    .disabled(purchasing)
                }

                Button("Restore Purchases") {
                    Task { await restore() }
                }
                .font(.callout)

                Button("I have a license key") {
                    // Phase 3: sheet zum License-Key paste/import
                }
                .font(.callout)

                if let msg = message {
                    Text(msg).font(.caption).foregroundStyle(.secondary)
                }
            }
            .padding()
        }
        .task {
            await loadProducts()
        }
    }

    private func loadProducts() async {
        do {
            products = try await Product.products(for: ["com.privycs.vpn.pro_lifetime"])
        } catch {
            message = "Failed to load: \(error.localizedDescription)"
        }
    }

    private func purchase(_ product: Product) async {
        purchasing = true
        defer { purchasing = false }
        do {
            let result = try await product.purchase()
            switch result {
            case .success(let verification):
                if case .verified(let transaction) = verification {
                    try await appState.entitlementRepo.markStoreKitEntitled(product.id)
                    await transaction.finish()
                    message = "Pro unlocked! Thank you."
                }
            case .userCancelled:
                break
            case .pending:
                message = "Purchase pending"
            @unknown default:
                break
            }
        } catch {
            message = error.localizedDescription
        }
    }

    private func restore() async {
        try? await AppStore.sync()
        message = "Restore initiated"
    }
}
