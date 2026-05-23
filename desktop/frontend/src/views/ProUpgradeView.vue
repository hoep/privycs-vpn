<template>
  <div class="pro-view">
    <header class="pro-header">
      <button class="back-btn" @click="$router.back()">← {{ t('common.back') }}</button>
      <h1>{{ t('pro.title') }}</h1>
      <span class="pro-badge">{{ t('pro.badge') }}</span>
    </header>

    <section v-if="entitlement.is_pro" class="active-card">
      <div class="active-icon">✓</div>
      <h2>{{ t('pro.active.title') }}</h2>
      <p>{{ t('pro.active.subtitle', { sku: entitlement.source }) }}</p>
      <p class="muted small">
        {{ t('pro.active.activatedOn', { date: entitlement.first_activated || '—' }) }}
      </p>
      <button class="danger-btn" @click="onDeactivate">{{ t('pro.active.deactivate') }}</button>
    </section>

    <section v-else>
      <ul class="feature-list">
        <li>{{ t('pro.features.multiProtocol') }}</li>
        <li>{{ t('pro.features.multiConfig') }}</li>
        <li>{{ t('pro.features.networkRules') }}</li>
        <li>{{ t('pro.features.gatewayDownload') }}</li>
        <li>{{ t('pro.features.pools') }}</li>
        <li>{{ t('pro.features.splitTunnel') }}</li>
      </ul>

      <div class="cta-row">
        <button class="primary-btn" @click="onBuyDesktop">
          {{ t('pro.buy.desktop', { price: '€9.99' }) }}
        </button>
        <button class="secondary-btn" @click="onBuyBundle">
          {{ t('pro.buy.bundle', { price: '€19.99' }) }}
        </button>
      </div>
      <p class="bundle-hint muted small">{{ t('pro.buy.bundleHint') }}</p>

      <hr class="divider" />

      <h3>{{ t('pro.activate.title') }}</h3>
      <p class="muted small">{{ t('pro.activate.subtitle') }}</p>
      <textarea
        v-model="licenseInput"
        :placeholder="t('pro.activate.placeholder')"
        rows="4"
      ></textarea>
      <div class="cta-row">
        <button class="primary-btn" :disabled="!licenseInput.trim() || activating" @click="onActivate">
          {{ activating ? t('pro.activate.activating') : t('pro.activate.activate') }}
        </button>
        <button class="secondary-btn" @click="onPickFile">
          {{ t('pro.activate.chooseFile') }}
        </button>
      </div>
      <p v-if="errorMessage" class="error">{{ errorMessage }}</p>
      <input
        ref="fileInput"
        type="file"
        accept=".privycs-license,.txt,.lic"
        style="display: none"
        @change="onFileChosen"
      />
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useEntitlement } from '@/composables/useEntitlement'

const { t } = useI18n()
const { entitlement, activate, deactivate, openStore } = useEntitlement()

const licenseInput = ref('')
const activating = ref(false)
const errorMessage = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

async function onActivate() {
  errorMessage.value = ''
  activating.value = true
  try {
    await activate(licenseInput.value.trim())
    licenseInput.value = ''
  } catch (e: any) {
    errorMessage.value = mapActivationError(e?.message || String(e))
  } finally {
    activating.value = false
  }
}

function mapActivationError(msg: string): string {
  // Map Go error wrappings into localised strings for the UI. We
  // match by substring rather than typed errors because Wails strings
  // the Go error through plain JS.
  if (msg.includes('signature')) return t('pro.errors.badSignature')
  if (msg.includes('platform')) return t('pro.errors.wrongPlatform')
  if (msg.includes('malformed')) return t('pro.errors.malformed')
  if (msg.includes('not supported') || msg.includes('version')) return t('pro.errors.unsupportedVersion')
  if (msg.includes('public key') || msg.includes('pubkey')) return t('pro.errors.noPublicKey')
  return t('pro.errors.generic', { msg })
}

async function onDeactivate() {
  await deactivate()
}

async function onBuyDesktop() {
  await openStore('privycs_pro_desktop')
}

async function onBuyBundle() {
  await openStore('privycs_pro_bundle_all')
}

function onPickFile() {
  fileInput.value?.click()
}

async function onFileChosen(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  const text = await f.text()
  // .privycs-license is a single PRVC-...-... line — paste into the
  // textarea so the user can review before hitting Activate.
  licenseInput.value = text.trim()
}
</script>

<style scoped>
.pro-view {
  padding: 24px;
  max-width: 720px;
  margin: 0 auto;
  color: var(--text-primary, #e8e8e8);
}
.pro-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
}
.pro-header h1 {
  flex: 1;
  font-size: 22px;
  margin: 0;
}
.pro-badge {
  background: linear-gradient(135deg, #6a5acd, #483d8b);
  color: white;
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.5px;
}
.back-btn {
  background: transparent;
  border: none;
  color: var(--text-secondary, #8c8c8c);
  cursor: pointer;
  font-size: 14px;
}
.active-card {
  background: rgba(72, 61, 139, 0.15);
  border: 1px solid rgba(106, 90, 205, 0.4);
  border-radius: 10px;
  padding: 24px;
  text-align: center;
}
.active-card h2 { margin: 8px 0; }
.active-icon {
  display: inline-block;
  width: 56px;
  height: 56px;
  border-radius: 50%;
  background: linear-gradient(135deg, #6a5acd, #483d8b);
  color: white;
  line-height: 56px;
  font-size: 28px;
}
.feature-list {
  list-style: none;
  padding: 0;
  margin: 16px 0;
}
.feature-list li {
  padding: 8px 0;
  border-bottom: 1px solid var(--border, #2a2a2a);
}
.feature-list li::before {
  content: "✓";
  color: #6a5acd;
  margin-right: 8px;
  font-weight: 700;
}
.cta-row {
  display: flex;
  gap: 12px;
  margin-top: 16px;
  flex-wrap: wrap;
}
.primary-btn,
.secondary-btn,
.danger-btn {
  padding: 10px 20px;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
  border: none;
  flex: 1;
  min-width: 140px;
}
.primary-btn {
  background: #6a5acd;
  color: white;
}
.primary-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.secondary-btn {
  background: transparent;
  border: 1px solid #6a5acd;
  color: #6a5acd;
}
.danger-btn {
  background: transparent;
  border: 1px solid #e74c3c;
  color: #e74c3c;
}
.divider {
  border: none;
  border-top: 1px solid var(--border, #2a2a2a);
  margin: 24px 0;
}
textarea {
  width: 100%;
  background: var(--bg-secondary, #1a1a1a);
  border: 1px solid var(--border, #2a2a2a);
  color: inherit;
  border-radius: 6px;
  padding: 10px;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  resize: vertical;
}
.bundle-hint,
.muted {
  color: var(--text-secondary, #8c8c8c);
}
.small { font-size: 12px; }
.error {
  color: #e74c3c;
  margin-top: 8px;
  font-size: 13px;
}
</style>
