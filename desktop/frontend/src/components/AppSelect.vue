<template>
  <Listbox :model-value="modelValue" @update:model-value="$emit('update:modelValue', $event)">
    <div class="relative">
      <ListboxButton
        class="relative w-full cursor-pointer rounded-md border border-gray-300 dark:border-gray-600
               bg-white dark:bg-gray-800 py-1.5 pl-3 pr-8 text-left text-sm
               text-gray-900 dark:text-gray-200
               focus:outline-none focus:ring-1 focus:ring-primary-500 focus:border-primary-500"
      >
        <span class="block truncate">{{ selectedLabel }}</span>
        <span class="pointer-events-none absolute inset-y-0 right-0 flex items-center pr-2">
          <ChevronUpDownIcon class="h-4 w-4 text-gray-400" />
        </span>
      </ListboxButton>

      <transition
        leave-active-class="transition duration-100 ease-in"
        leave-from-class="opacity-100"
        leave-to-class="opacity-0"
      >
        <ListboxOptions
          class="absolute z-20 mt-1 max-h-60 w-full overflow-auto rounded-md
                 bg-white dark:bg-gray-800 py-1 text-sm shadow-lg
                 ring-1 ring-black/5 dark:ring-white/10
                 focus:outline-none"
        >
          <ListboxOption
            v-for="option in options"
            :key="option.value"
            :value="option.value"
            v-slot="{ active, selected }"
            as="template"
          >
            <li
              class="relative cursor-pointer select-none py-2 pl-3 pr-9 transition-colors"
              :class="active ? 'bg-primary-500/10 text-primary-600 dark:text-primary-400' : 'text-gray-900 dark:text-gray-200'"
            >
              <span class="block truncate" :class="selected ? 'font-semibold' : 'font-normal'">
                {{ option.label }}
              </span>
              <span v-if="selected" class="absolute inset-y-0 right-0 flex items-center pr-3 text-primary-500">
                <CheckIcon class="h-4 w-4" />
              </span>
            </li>
          </ListboxOption>
        </ListboxOptions>
      </transition>
    </div>
  </Listbox>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Listbox, ListboxButton, ListboxOption, ListboxOptions } from '@headlessui/vue'
import { CheckIcon, ChevronUpDownIcon } from '@heroicons/vue/24/outline'

const props = defineProps<{
  modelValue: string
  options: { value: string; label: string }[]
}>()

defineEmits(['update:modelValue'])

const selectedLabel = computed(() => {
  const opt = props.options.find(o => o.value === props.modelValue)
  return opt?.label || props.modelValue
})
</script>
