<template>
  <Teleport to="body">
    <div v-if="visible" class="dialog-backdrop" @click.self="$emit('close')">
      <div class="dialog">
        <div class="dialog-header">
          <h2>{{ t('upgradeDialog.title') }}</h2>
          <button class="close-btn" @click="$emit('close')">×</button>
        </div>
        <div class="dialog-body">
          <p>{{ t('upgradeDialog.body', { feature: featureLabel || t('upgradeDialog.thisFeature') }) }}</p>
          <p class="muted small">{{ t('upgradeDialog.subtitle') }}</p>
        </div>
        <div class="dialog-footer">
          <button class="secondary-btn" @click="$emit('close')">
            {{ t('upgradeDialog.notNow') }}
          </button>
          <button class="primary-btn" @click="goToPro">
            {{ t('upgradeDialog.viewPro') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

defineProps<{
  visible: boolean
  featureLabel?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()
const router = useRouter()

function goToPro() {
  emit('close')
  router.push({ name: 'Pro' })
}
</script>

<style scoped>
.dialog-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.dialog {
  background: var(--bg-primary, #1a1a1a);
  color: var(--text-primary, #e8e8e8);
  border-radius: 10px;
  width: 440px;
  max-width: 90vw;
  border: 1px solid var(--border, #2a2a2a);
  overflow: hidden;
}
.dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border, #2a2a2a);
}
.dialog-header h2 {
  margin: 0;
  font-size: 16px;
}
.close-btn {
  background: transparent;
  border: none;
  color: var(--text-secondary, #8c8c8c);
  font-size: 24px;
  cursor: pointer;
  line-height: 1;
}
.dialog-body {
  padding: 20px;
}
.dialog-body p {
  margin: 0 0 8px 0;
}
.muted { color: var(--text-secondary, #8c8c8c); }
.small { font-size: 12px; }
.dialog-footer {
  display: flex;
  gap: 12px;
  padding: 16px 20px;
  border-top: 1px solid var(--border, #2a2a2a);
  justify-content: flex-end;
}
.primary-btn,
.secondary-btn {
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 14px;
  cursor: pointer;
  border: none;
  font-weight: 600;
}
.primary-btn {
  background: #6a5acd;
  color: white;
}
.secondary-btn {
  background: transparent;
  border: 1px solid var(--border, #2a2a2a);
  color: inherit;
}
</style>
