<script setup lang="ts">
import { computed, ref } from 'vue'
import type { IVRVariableDefinition } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Braces, Search } from 'lucide-vue-next'

const props = defineProps<{
  variables: IVRVariableDefinition[]
}>()

const emit = defineEmits<{
  select: [variable: IVRVariableDefinition]
}>()

const open = ref(false)
const search = ref('')

const grouped = computed(() => {
  const needle = search.value.trim().toLowerCase()
  const groups = new Map<string, IVRVariableDefinition[]>()
  for (const variable of props.variables || []) {
    if (needle && !`${variable.label} ${variable.path} ${variable.source}`.toLowerCase().includes(needle)) continue
    if (!groups.has(variable.source)) groups.set(variable.source, [])
    groups.get(variable.source)!.push(variable)
  }
  return Array.from(groups.entries()).map(([source, variables]) => ({ source, variables }))
})

function choose(variable: IVRVariableDefinition) {
  emit('select', variable)
  open.value = false
  search.value = ''
}
</script>

<template>
  <div class="relative">
    <Button type="button" variant="outline" size="sm" class="h-7 text-xs" @click="open = !open">
      <Braces class="h-3 w-3" /> Insert Variable
    </Button>

    <div v-if="open" class="mt-2 border rounded-lg bg-background shadow-sm overflow-hidden">
      <div class="p-2 border-b relative">
        <Search class="absolute left-4 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
        <Input v-model="search" placeholder="Search variables..." class="h-7 text-xs pl-7" />
      </div>
      <div class="max-h-56 overflow-y-auto p-1.5">
        <div v-if="grouped.length === 0" class="px-2 py-3 text-[11px] text-muted-foreground text-center">
          No variables are available before this node.
        </div>
        <div v-for="group in grouped" :key="group.source" class="mb-2 last:mb-0">
          <div class="px-2 py-1 text-[10px] uppercase tracking-wide text-muted-foreground font-medium">{{ group.source }}</div>
          <button
            v-for="variable in group.variables"
            :key="variable.path"
            type="button"
            class="w-full text-left px-2 py-1.5 rounded-md hover:bg-muted flex items-center gap-2"
            @click="choose(variable)"
          >
            <span class="inline-flex max-w-[145px] truncate rounded-full border bg-muted/50 px-2 py-0.5 text-[10px] font-medium">{{ variable.label }}</span>
            <code class="text-[10px] text-muted-foreground truncate">{{ variable.path }}</code>
            <span v-if="variable.type" class="ml-auto text-[9px] text-muted-foreground shrink-0">{{ variable.type }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
