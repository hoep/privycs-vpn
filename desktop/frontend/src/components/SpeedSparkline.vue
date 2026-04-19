<template>
  <!-- Rudimentary sparkline for upload/download speed. No axes, no
       labels, no tooltip - just the area-filled curve. The parent
       card holds the current-speed number separately. -->
  <div ref="chartEl" class="w-full h-8"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

// Register only the modules we need so the bundle stays as small as
// echarts' tree-shakeable core allows (~60 KB gzipped, vs. ~200 KB
// for the full bundle).
echarts.use([LineChart, GridComponent, CanvasRenderer])

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
    xAxis: { type: 'category', show: false, boundaryGap: false },
    yAxis: { type: 'value', show: false, min: 0 },
    series: [
      {
        type: 'line',
        smooth: true,
        symbol: 'none',
        lineStyle: { color: props.color, width: 1.5 },
        areaStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: props.color + 'cc' }, // 80% alpha
            { offset: 1, color: props.color + '00' }, //  0% alpha
          ]),
        },
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
