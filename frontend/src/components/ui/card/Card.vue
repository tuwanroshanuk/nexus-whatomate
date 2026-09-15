<script setup lang="ts">
import { cn } from '@/lib/utils'

const props = defineProps<{
  class?: string
}>()
</script>

<template>
  <div
    :class="cn(
      'rounded-xl border border-white/[0.06] bg-white/[0.02] text-card-foreground backdrop-blur-sm transition-colors duration-300 hover:border-white/[0.15] hover:bg-white/[0.04] light:bg-white light:border-gray-200 light:hover:border-gray-300',
      props.class
    )"
  >
    <slot />
  </div>
</template>
