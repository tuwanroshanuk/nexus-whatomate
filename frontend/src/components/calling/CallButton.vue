<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useCallingStore } from '@/stores/calling'
import { outgoingCallsService } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Phone, PhoneCall, ShieldCheck, Send, Loader2, Clock, X, ChevronDown } from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const props = defineProps<{
  contactId: string
  contactPhone: string
  contactName: string
  whatsappAccount: string
}>()

const { t } = useI18n()
const store = useCallingStore()
const isInitiating = ref(false)

const permission = computed(() => store.getCallPermission(props.contactId))
const hasPermission = computed(() => permission.value?.status === 'accepted')

onMounted(async () => {
  if (permission.value) return
  try {
    const resp = await outgoingCallsService.getPermission(props.contactId, props.whatsappAccount)
    const perm = (resp.data as any).data ?? resp.data
    if (perm.status === 'temporary' || perm.status === 'permanent') {
      store.callPermissions.set(props.contactId, {
        status: 'accepted',
        expiresAt: perm.expires_at,
      })
    } else if (perm.status === 'declined') {
      store.callPermissions.set(props.contactId, { status: 'declined' })
    } else if (perm.status === 'pending') {
      store.callPermissions.set(props.contactId, { status: 'pending' })
    }
  } catch {
    // No permission record — leave as default.
  }
})

const tooltipText = computed(() => {
  if (store.isOnCall) return t('outgoingCalls.callButtonDisabled')
  if (isInitiating.value) return 'Starting voice call…'
  if (permission.value?.status === 'pending') return 'Call permission requested (pending customer reply)'
  if (permission.value?.status === 'accepted') return 'Call permission active — click to start call'
  if (permission.value?.status === 'declined') return 'Call permission declined — use the menu to request it again'
  return 'Request call permission from the menu first'
})

async function startDirectCall() {
  if (store.isOnCall || isInitiating.value || !hasPermission.value) return
  isInitiating.value = true
  try {
    await store.makeOutgoingCall(props.contactId, props.contactName, props.whatsappAccount)
  } catch (err: any) {
    handleError(err, 'Call Failed')
  } finally {
    isInitiating.value = false
  }
}

async function sendPermissionRequest() {
  if (isInitiating.value) return
  isInitiating.value = true
  try {
    await outgoingCallsService.requestPermission({
      contact_id: props.contactId,
      whatsapp_account: props.whatsappAccount,
      method: 'interactive',
    })
    store.setCallPermissionPending(props.contactId)
    toast.success('Permission Request Sent', {
      description: 'An interactive call permission prompt was sent to the contact in WhatsApp.',
    })
  } catch (err: any) {
    handleError(err, 'Permission Request Failed')
  } finally {
    isInitiating.value = false
  }
}

async function sendCallRequestTemplate() {
  if (isInitiating.value) return
  isInitiating.value = true
  try {
    await outgoingCallsService.requestPermission({
      contact_id: props.contactId,
      whatsapp_account: props.whatsappAccount,
      method: 'template',
    })
    store.setCallPermissionPending(props.contactId)
    toast.success('Call Template Sent', {
      description: "Approved 'request_call_permission' template with Call button was sent to contact.",
    })
  } catch (err: any) {
    handleError(err, 'Template Send Failed')
  } finally {
    isInitiating.value = false
  }
}

function handleError(err: any, title: string) {
  const msg = err.response?.data?.error?.message || err.response?.data?.message || err.message || String(err)
  if (msg.includes('190') || msg.toLowerCase().includes('token expired') || msg.toLowerCase().includes('authentication')) {
    toast.error('Meta Access Token Expired', {
      description: 'Your WhatsApp Meta API Access Token is expired or invalid. Please update it in Settings > Meta App Credentials.',
      duration: 8000,
    })
  } else {
    toast.error(title, { description: msg })
  }
}
</script>

<template>
  <div class="flex items-center">
    <Tooltip>
      <TooltipTrigger as-child>
        <Button
          variant="ghost"
          size="sm"
          class="h-8 px-2 rounded-r-none relative text-white/70 hover:text-white hover:bg-white/[0.08] light:text-gray-600 light:hover:text-gray-900 light:hover:bg-gray-100"
          :class="hasPermission ? 'text-emerald-400 hover:text-emerald-300 bg-emerald-500/10 border border-emerald-500/20' : ''"
          :disabled="store.isOnCall || isInitiating || !hasPermission"
          @click="startDirectCall"
        >
          <Loader2 v-if="isInitiating" class="h-4 w-4 animate-spin text-emerald-400" />
          <Phone v-else class="h-4 w-4" :class="hasPermission ? 'text-emerald-400' : ''" />

          <span
            v-if="permission?.status === 'pending'"
            class="absolute -top-0.5 -right-0.5 flex h-3 w-3 items-center justify-center rounded-full bg-amber-500 text-white"
            title="Permission Pending"
          >
            <Clock class="h-2 w-2" />
          </span>
          <span
            v-else-if="permission?.status === 'declined'"
            class="absolute -top-0.5 -right-0.5 flex h-3 w-3 items-center justify-center rounded-full bg-red-500 text-white"
            title="Permission Declined"
          >
            <X class="h-2 w-2" />
          </span>
        </Button>
      </TooltipTrigger>
      <TooltipContent>{{ tooltipText }}</TooltipContent>
    </Tooltip>

    <DropdownMenu>
      <DropdownMenuTrigger as-child>
        <Button
          variant="ghost"
          size="sm"
          aria-label="Voice call options"
          class="h-8 w-6 px-0 rounded-l-none border-l border-white/[0.08] text-white/60 hover:text-white hover:bg-white/[0.08] light:border-gray-200 light:text-gray-500 light:hover:text-gray-900 light:hover:bg-gray-100"
          :disabled="store.isOnCall || isInitiating"
        >
          <ChevronDown class="h-3 w-3" />
        </Button>
      </DropdownMenuTrigger>

      <DropdownMenuContent align="end" class="w-64 bg-[#141414] border-white/[0.1] text-white light:bg-white light:border-gray-200 light:text-gray-900 shadow-xl">
        <DropdownMenuLabel class="text-xs text-white/50 light:text-gray-500 uppercase tracking-wider font-semibold">
          Voice Call Actions
        </DropdownMenuLabel>
        <DropdownMenuSeparator class="bg-white/[0.08] light:bg-gray-200" />

        <DropdownMenuItem
          class="gap-2.5 py-2.5 cursor-pointer focus:bg-emerald-500/10 focus:text-emerald-300 light:focus:bg-emerald-50 light:focus:text-emerald-700"
          :disabled="!hasPermission"
          @click="startDirectCall"
        >
          <div class="w-7 h-7 rounded-md bg-emerald-500/20 text-emerald-400 flex items-center justify-center">
            <PhoneCall class="h-3.5 w-3.5" />
          </div>
          <div>
            <p class="text-sm font-medium">Start Voice Call</p>
            <p class="text-[11px] text-white/40 light:text-gray-500">
              {{ hasPermission ? 'Permission active — call now' : 'Requires accepted permission' }}
            </p>
          </div>
        </DropdownMenuItem>

        <DropdownMenuSeparator class="bg-white/[0.08] light:bg-gray-200" />

        <DropdownMenuItem
          class="gap-2.5 py-2.5 cursor-pointer focus:bg-sky-500/10 focus:text-sky-300 light:focus:bg-sky-50 light:focus:text-sky-700"
          @click="sendPermissionRequest"
        >
          <div class="w-7 h-7 rounded-md bg-sky-500/20 text-sky-400 flex items-center justify-center">
            <ShieldCheck class="h-3.5 w-3.5" />
          </div>
          <div>
            <p class="text-sm font-medium">Request Call Permission</p>
            <p class="text-[11px] text-white/40 light:text-gray-500">Interactive prompt (72h validity)</p>
          </div>
        </DropdownMenuItem>

        <DropdownMenuItem
          class="gap-2.5 py-2.5 cursor-pointer focus:bg-violet-500/10 focus:text-violet-300 light:focus:bg-violet-50 light:focus:text-violet-700"
          @click="sendCallRequestTemplate"
        >
          <div class="w-7 h-7 rounded-md bg-violet-500/20 text-violet-400 flex items-center justify-center">
            <Send class="h-3.5 w-3.5" />
          </div>
          <div>
            <p class="text-sm font-medium">Send Call Request Template</p>
            <p class="text-[11px] text-white/40 light:text-gray-500">Sends template with Call button</p>
          </div>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>

    <Teleport to="body">
      <div
        v-if="isInitiating"
        class="fixed bottom-6 right-6 z-[60] min-w-[260px] rounded-xl border border-zinc-700 bg-zinc-900 p-4 shadow-2xl"
      >
        <div class="flex items-center gap-3">
          <div class="flex h-9 w-9 items-center justify-center rounded-full bg-emerald-600/20">
            <Loader2 class="h-4 w-4 animate-spin text-emerald-400" />
          </div>
          <div>
            <p class="text-sm font-medium text-zinc-100">{{ contactName }}</p>
            <p class="text-xs text-zinc-400">Starting voice call…</p>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
