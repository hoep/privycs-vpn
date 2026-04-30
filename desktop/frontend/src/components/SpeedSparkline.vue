<template>
  <!-- Bar-style sparkline for upload/download speed. One vertical bar
       per sample. No axes, no labels, no tooltip. Parent card holds
       the current-speed number separately. -->
  <div ref="chartEl" class="w-full h-8"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
import * as echarts from 'echarts/core'
import { BarChart } from 'echarts/charts'
import { GridComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

// Register only the modules we need so the bundle stays small.
// Switched from LineChart to BarChart to communicate VPN's bursty
// throughput pattern more honestly: a smooth area-curve smooths
// individual page-load / video-chunk spikes into a misleading
// shape, while bars preserve sample-discrete resolution. Mirrors
// dashboard convention (Grafana, DataDog) for throughput strips.
echarts.use([BarChart, GridComponent, CanvasRenderer])

const props = defineProps<{
  data: number[]
  color: string // base hex without alpha, e.g. "#4ade80"
}>()

const chartEl = ref<HTMLElement | null>(null)
let chart: echarts.ECharts | null = null

function buildOption() {
  return {
    animation: false,
    grid: { left: 0, right: 0, top: 0, bottom: 0, containLabel: false },
    xAxis: { type: 'category', show: false, boundaryGap: true },
    yAxis: { type: 'value', show: false, min: 0 },
    series: [
      {
        type: 'bar',
        // 70% category width = 30% gap between bars - the
        // "discrete sample" reading we want, vs a continuous fill.
        barCategoryGap: '30%',
        // 1.5px floor so a tiny non-zero burst stays visible
        // instead of vanishing into the baseline.
        barMinHeight: 1.5,
        itemStyle: { color: props.color, borderRadius: [1, 1, 0, 0] },
        data: props.data,
      },
    ],
  }
}

function render() {
  if (!chart) return
  chart.setOption(buildOption(), { notMerge: false, lazyUpdate: false })
}

onMounted(() => {
  if (!chartEl.value) return
  chart = echarts.init(chartEl.value, null, { renderer: 'canvas' })
  render()
  // Echarts doesn't auto-resize when the parent card shrinks on a
  // window-resize; observe and resize explicitly. ResizeObserver is
  // cheaper than a window event listener plus more accurate.
  const ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(chartEl.value)
  // Keep the observer alive for the component lifetime.
  ;(chart as any).__ro = ro
})

onBeforeUnmount(() => {
  if (chart) {
    const ro = (chart as any).__ro as ResizeObserver | undefined
    ro?.disconnect()
    chart.dispose()
    chart = null
  }
})

// Re-render when the input data array changes. The array is replaced
// on every poll (not mutated in place) so Vue's reactivity triggers
// this watch reliably.
watch(
  () => props.data,
  () => render(),
  { deep: false }
)
</script>
