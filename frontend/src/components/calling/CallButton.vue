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

// Fetch existing permission status on mount
onMounted(async () => {
  if (permission.value) return // already known
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
    // No permission record — leave as default
  }
})

const tooltipText = computed(() => {
  if (store.isOnCall) return t('outgoingCalls.callButtonDisabled')
  if (permission.value?.status === 'pending') return 'Call permission requested (pending customer reply)'
  if (permission.value?.status === 'accepted') return 'Call permission active — click to start call'
  if (permission.value?.status === 'declined') return 'Call permission declined by customer'
  return 'Voice Call options'
})

// Action 1: Make direct WebRTC call
async function startDirectCall() {
  if (store.isOnCall || isInitiating.value) return
  isInitiating.value = true
  try {
    await store.makeOutgoingCall(props.contactId, props.contactName, props.whatsappAccount)
  } catch (err: any) {
    handleError(err, 'Call Failed')
  } finally {
    isInitiating.value = false
  }
}

// Action 2: Send interactive call permission prompt
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

// Action 3: Send Call Request Template ('request_call_permission')
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
  const msg = err.response?.data?.error?.message || err.message || String(err)
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
  <DropdownMenu>
    <Tooltip>
      <TooltipTrigger as-child>
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            size="sm"
            class="h-8 px-2 gap-1 relative text-white/70 hover:text-white hover:bg-white/[0.08] light:text-gray-600 light:hover:text-gray-900 light:hover:bg-gray-100"
            :class="[
              permission?.status === 'accepted'
                ? 'text-emerald-400 hover:text-emerald-300 bg-emerald-500/10 border border-emerald-500/20'
                : ''
            ]"
            :disabled="store.isOnCall || isInitiating"
          >
            <Loader2 v-if="isInitiating" class="h-4 w-4 animate-spin text-emerald-400" />
            <Phone v-else class="h-4 w-4" :class="permission?.status === 'accepted' ? 'text-emerald-400' : ''" />
            <ChevronDown class="h-3 w-3 opacity-60" />

            <!-- Permission status indicator dot -->
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
        </DropdownMenuTrigger>
      </TooltipTrigger>
      <TooltipContent>{{ tooltipText }}</TooltipContent>
    </Tooltip>

    <DropdownMenuContent align="end" class="w-64 bg-[#141414] border-white/[0.1] text-white light:bg-white light:border-gray-200 light:text-gray-900 shadow-xl">
      <DropdownMenuLabel class="text-xs text-white/50 light:text-gray-500 uppercase tracking-wider font-semibold">
        Voice Call Actions
      </DropdownMenuLabel>
      <DropdownMenuSeparator class="bg-white/[0.08] light:bg-gray-200" />

      <!-- Direct Call -->
      <DropdownMenuItem
        class="gap-2.5 py-2.5 cursor-pointer focus:bg-emerald-500/10 focus:text-emerald-300 light:focus:bg-emerald-50 light:focus:text-emerald-700"
        @click="startDirectCall"
      >
        <div class="w-7 h-7 rounded-md bg-emerald-500/20 text-emerald-400 flex items-center justify-center">
          <PhoneCall class="h-3.5 w-3.5" />
        </div>
        <div>
          <p class="text-sm font-medium">Start Voice Call</p>
          <p class="text-[11px] text-white/40 light:text-gray-500">Initiate WebRTC call now</p>
        </div>
      </DropdownMenuItem>

      <DropdownMenuSeparator class="bg-white/[0.08] light:bg-gray-200" />

      <!-- Interactive Permission Prompt -->
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

      <!-- Template Call Request -->
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
</template>

