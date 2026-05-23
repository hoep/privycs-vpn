<template>
  <!-- Full-screen modal webcam QR scanner. Uses the Chromium-native
       BarcodeDetector API which is available in Wails' WebView2 on
       Windows and in Chromium on Linux. When the API is unreachable
       (Safari / WebKitGTK) the modal falls back to an error message
       with the raw-paste textarea as a usable escape hatch. -->
  <div class="fixed inset-0 bg-black/80 flex items-center justify-center z-50 p-4" @click.self="close">
    <div class="bg-white dark:bg-gray-900 rounded-xl shadow-2xl w-full max-w-md flex flex-col">
      <div class="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ $t('components.qr-scan-modal.title') }}</h3>
        <button @click="close" class="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
          <XMarkIcon class="w-4 h-4" />
        </button>
      </div>
      <div class="p-4">
        <div v-if="error" class="mb-3">
          <p class="text-xs text-red-400 mb-3">{{ error }}</p>
          <!-- Fallback for unsupported platforms: paste-raw escape hatch.
               Still useful because the user can copy the QR content from
               another device (phone, other laptop) and paste it here. -->
          <label class="block text-xs text-gray-500 dark:text-gray-400 mb-1">{{ $t('components.qr-scan-modal.paste-label') }}</label>
          <textarea
            v-model="pasted"
            rows="4"
            class="w-full bg-gray-50 dark:bg-gray-800 text-xs font-mono p-2 rounded border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-2 focus:ring-primary-500 resize-none"
            :placeholder="$t('components.qr-scan-modal.paste-placeholder')"
          />
          <button
            @click="submitPasted"
            :disabled="!pasted.trim()"
            class="btn-primary mt-2 w-full py-2 text-xs disabled:opacity-40"
          >
            {{ $t('components.qr-scan-modal.button.use-pasted') }}
          </button>
        </div>
        <div v-else>
          <div class="relative aspect-square bg-black rounded-lg overflow-hidden">
            <video
              ref="videoEl"
              autoplay
              muted
              playsinline
              class="w-full h-full object-cover"
            />
            <!-- Center-ish scanning reticle. Shrunken view rectangle
                 hints to the user where to align the QR. -->
            <div class="absolute inset-8 border-2 border-white/40 rounded-lg pointer-events-none"></div>
          </div>
          <p class="text-[10px] text-gray-500 dark:text-gray-400 text-center mt-2">
            {{ $t('components.qr-scan-modal.hint') }}
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { XMarkIcon } from '@heroicons/vue/24/outline'

const { t } = useI18n()

const emit = defineEmits<{
  (e: 'scanned', raw: string): void
  (e: 'close'): void
}>()

const videoEl = ref<HTMLVideoElement | null>(null)
const error = ref('')
const pasted = ref('')
let stream: MediaStream | null = null
let detector: any = null
let frameHandle: number | null = null
let stopped = false

function close() {
  stopped = true
  stopCamera()
  emit('close')
}

function submitPasted() {
  const text = pasted.value.trim()
  if (text) {
    emit('scanned', text)
  }
}

function stopCamera() {
  if (frameHandle !== null) {
    cancelAnimationFrame(frameHandle)
    frameHandle = null
  }
  if (stream) {
    stream.getTracks().forEach(t => t.stop())
    stream = null
  }
}

async function startCamera() {
  // Feature-detect BarcodeDetector. Not available in WebKit (macOS
  // Safari / WebKitGTK on Linux with older versions) — those paths
  // fall through to the paste-raw escape hatch instead of pretending
  // to scan and never finding anything.
  const hasDetector = typeof (globalThis as any).BarcodeDetector !== 'undefined'
  if (!hasDetector) {
    error.value = t('components.qr-scan-modal.error.no-detector')
    return
  }
  try {
    // Query supported formats. If qr_code isn't in the list, bail early.
    const formats: string[] = await (globalThis as any).BarcodeDetector.getSupportedFormats()
    if (!formats.includes('qr_code')) {
      error.value = t('components.qr-scan-modal.error.qr-not-supported')
      return
    }
    detector = new (globalThis as any).BarcodeDetector({ formats: ['qr_code'] })
  } catch (e: any) {
    error.value = t('components.qr-scan-modal.error.init-failed', { error: String(e?.message || e) })
    return
  }

  try {
    stream = await navigator.mediaDevices.getUserMedia({
      video: { facingMode: 'environment' },
      audio: false,
    })
    if (videoEl.value) {
      videoEl.value.srcObject = stream
      await videoEl.value.play()
    }
  } catch (e: any) {
    error.value = e?.name === 'NotAllowedError'
      ? t('components.qr-scan-modal.error.camera-denied')
      : t('components.qr-scan-modal.error.camera-failed', { error: String(e?.message || e) })
    stopCamera()
    return
  }

  // requestAnimationFrame loop — throttles itself to the display
  // refresh rate so we don't burn CPU. Each frame is piped to the
  // detector; first successful detection emits and closes.
  const tick = async () => {
    if (stopped || !detector || !videoEl.value) return
    try {
      const codes = await detector.detect(videoEl.value)
      if (codes && codes.length > 0 && codes[0].rawValue) {
        emit('scanned', codes[0].rawValue)
        stopCamera()
        stopped = true
        return
      }
    } catch {
      // Transient detector errors (e.g. frame not ready) happen often;
      // just try again next frame.
    }
    frameHandle = requestAnimationFrame(tick)
  }
  frameHandle = requestAnimationFrame(tick)
}

onMounted(() => {
  startCamera()
})

onBeforeUnmount(() => {
  stopped = true
  stopCamera()
})
</script>
