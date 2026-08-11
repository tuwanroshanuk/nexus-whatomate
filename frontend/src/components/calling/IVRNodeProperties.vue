<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue' 
import type { IVRNode, IVRNodeType, IVRVariableDefinition, IVRHTTPDiscoveredVariable } from '@/services/api'
import { ivrFlowsService, ttsService, type TTSModelInfo, type TTSSettings, type GoogleCloudTTSVoice } from '@/services/api' 
import { useCallingStore } from '@/stores/calling'
import { useTeamsStore } from '@/stores/teams'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Trash2, Plus, Upload, Play, Pause, X, Loader2, Type, FlaskConical } from 'lucide-vue-next'
import IVRVariablePicker from '@/components/calling/IVRVariablePicker.vue'
import { toast } from 'vue-sonner'

const props = defineProps<{
  node: IVRNode
  currentFlowId?: string
  availableVariables?: IVRVariableDefinition[]
}>()

const emit = defineEmits<{
  'update:node': [node: IVRNode]
  'delete': []
}>()

const callingStore = useCallingStore()
const teamsStore = useTeamsStore()

if (teamsStore.teams.length === 0) teamsStore.fetchTeams()

const config = computed(() => props.node.config || {})

const ttsModels = ref<TTSModelInfo[]>([])
const ttsDefaults = ref<TTSSettings | null>(null)
const googleCloudVoices = ref<GoogleCloudTTSVoice[]>([])
const effectiveTTSProvider = computed(() => String(config.value.tts_provider || ttsDefaults.value?.default_provider || 'local'))
const geminiModels = [['gemini-3.1-flash-tts-preview', 'Gemini 3.1 Flash TTS Preview'], ['gemini-2.5-flash-preview-tts', 'Gemini 2.5 Flash Preview TTS'], ['gemini-2.5-pro-preview-tts', 'Gemini 2.5 Pro Preview TTS']]
const geminiVoices = ['Kore','Puck','Charon','Zephyr','Fenrir','Leda','Orus','Aoede','Callirrhoe','Autonoe','Enceladus','Iapetus','Umbriel','Algieba','Despina','Erinome','Algenib','Rasalgethi','Laomedeia','Achernar','Alnilam','Schedar','Gacrux','Pulcherrima','Achird','Zubenelgenubi','Vindemiatrix','Sadachbia','Sadaltager','Sulafat']
const primaryTTSLanguages = [['si-LK','Sinhala (Sri Lanka)'],['en-US','English (United States)'],['ko-KR','Korean (South Korea)'],['es-ES','Spanish (Spain)'],['ja-JP','Japanese (Japan)']]
const effectiveCloudLanguage = computed(() => String(config.value.tts_google_cloud_language || ttsDefaults.value?.google_cloud_language || 'en-US'))
const filteredNodeCloudVoices = computed(() => effectiveCloudLanguage.value === 'si-LK' ? [] : googleCloudVoices.value.filter(v => v.language_codes?.includes(effectiveCloudLanguage.value)))

async function loadTTSOptions() {
  try {
    const res = await ttsService.getSettings()
    const data = (res.data as any)?.data || res.data
    ttsModels.value = data.models || []
    ttsDefaults.value = data.settings || null
    if (data.providers?.google_cloud?.configured) {
      try {
        const voicesRes = await ttsService.getGoogleCloudVoices()
        const voicesData = (voicesRes.data as any)?.data || voicesRes.data
        googleCloudVoices.value = voicesData.voices || []
      } catch {
        // Voice discovery is optional; provider settings remain editable.
      }
    }
  } catch {
    // TTS can be disabled server-side; keep the normal audio editor usable.
  }
}

onMounted(loadTTSOptions)

function updateConfig(key: string, value: any) {
  emit('update:node', {
    ...props.node,
    config: { ...props.node.config, [key]: value }
  })
}

// Set multiple config keys in a single emit. Calling updateConfig twice in a
// row loses the first change: the prop doesn't update synchronously after
// emit, so the second call rebuilds from a stale props.node and clobbers it.
function updateConfigEntries(entries: Record<string, any>) {
  emit('update:node', {
    ...props.node,
    config: { ...props.node.config, ...entries }
  })
}

function updateLabel(label: string) {
  emit('update:node', { ...props.node, label })
}

// Audio upload state
const audioFileInput = ref<HTMLInputElement | null>(null)
const isUploading = ref(false)
const isPlaying = ref(false)
const audioElement = ref<HTMLAudioElement | null>(null)

function triggerFileUpload() {
  audioFileInput.value?.click()
}

async function handleFileSelect(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  if (file.size > 5 * 1024 * 1024) {
    toast.error('File too large (max 5MB)')
    input.value = ''
    return
  }
  isUploading.value = true
  try {
    const res = await ivrFlowsService.uploadAudio(file)
    const filename = res.data?.data?.filename
    if (filename) {
      // Uploading a clip and TTS text are mutually exclusive; clear greeting_text
      // so the backend doesn't TTS-regenerate and overwrite the uploaded audio_file.
      updateConfigEntries({ audio_file: filename, greeting_text: undefined })
      toast.success('Audio uploaded')
    }
  } catch {
    toast.error('Upload failed')
  } finally {
    isUploading.value = false
    input.value = ''
  }
}

function removeAudio() {
  stopAudio()
  updateConfig('audio_file', '')
}

function togglePlayback() {
  if (isPlaying.value) stopAudio()
  else playAudio()
}

function playAudio() {
  if (!config.value.audio_file) return
  stopAudio()
  const audio = new Audio(ivrFlowsService.getAudioUrl(config.value.audio_file))
  audio.onended = () => { isPlaying.value = false }
  audio.onerror = () => { isPlaying.value = false }
  audio.play()
  audioElement.value = audio
  isPlaying.value = true
}

function stopAudio() {
  if (audioElement.value) {
    audioElement.value.pause()
    audioElement.value = null
  }
  isPlaying.value = false
}


// HTTP callback progress audio state. This file is looped to the caller only
// while the live HTTP request is running.
const progressAudioFileInput = ref<HTMLInputElement | null>(null)
const isUploadingProgressAudio = ref(false)
const isPlayingProgressAudio = ref(false)
const progressAudioElement = ref<HTMLAudioElement | null>(null)

function triggerProgressAudioUpload() {
  progressAudioFileInput.value?.click()
}

async function handleProgressAudioFileSelect(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  if (file.size > 5 * 1024 * 1024) {
    toast.error('File too large (max 5MB)')
    input.value = ''
    return
  }
  isUploadingProgressAudio.value = true
  try {
    const res = await ivrFlowsService.uploadAudio(file)
    const filename = res.data?.data?.filename
    if (filename) {
      updateConfig('progress_audio_file', filename)
      toast.success('Progress audio uploaded')
    }
  } catch {
    toast.error('Progress audio upload failed')
  } finally {
    isUploadingProgressAudio.value = false
    input.value = ''
  }
}

function removeProgressAudio() {
  stopProgressAudio()
  updateConfig('progress_audio_file', '')
}

function toggleProgressAudioPlayback() {
  if (isPlayingProgressAudio.value) stopProgressAudio()
  else playProgressAudio()
}

function playProgressAudio() {
  const filename = String(config.value.progress_audio_file || '')
  if (!filename) return
  stopProgressAudio()
  const audio = new Audio(ivrFlowsService.getAudioUrl(filename))
  audio.loop = true
  audio.onended = () => { isPlayingProgressAudio.value = false }
  audio.onerror = () => { isPlayingProgressAudio.value = false }
  audio.play()
  progressAudioElement.value = audio
  isPlayingProgressAudio.value = true
}

function stopProgressAudio() {
  if (progressAudioElement.value) {
    progressAudioElement.value.pause()
    progressAudioElement.value.currentTime = 0
    progressAudioElement.value = null
  }
  isPlayingProgressAudio.value = false
}

// Menu options helpers
function addMenuOption() {
  const opts = { ...(config.value.options || {}) }
  const used = new Set(Object.keys(opts))
  const digit = ['1','2','3','4','5','6','7','8','9','0','*','#'].find(d => !used.has(d))
  if (!digit) return
  opts[digit] = { label: '' }
  updateConfig('options', opts)
}

function removeMenuOption(digit: string) {
  const opts = { ...(config.value.options || {}) }
  delete opts[digit]
  updateConfig('options', opts)
}

function updateMenuOption(digit: string, field: string, value: string) {
  const opts = { ...(config.value.options || {}) }
  opts[digit] = { ...opts[digit], [field]: value }
  updateConfig('options', opts)
}

// Timing schedule helpers
const defaultSchedule = [
  { day: 'monday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'tuesday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'wednesday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'thursday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'friday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'saturday', enabled: false, start_time: '09:00', end_time: '17:00' },
  { day: 'sunday', enabled: false, start_time: '09:00', end_time: '17:00' },
]

const schedule = computed(() => config.value.schedule || defaultSchedule)

function updateScheduleEntry(idx: number, field: string, value: any) {
  const sched = [...schedule.value]
  sched[idx] = { ...sched[idx], [field]: value }
  updateConfig('schedule', sched)
}

// HTTP headers helpers
function addHeader() {
  const headers = { ...(config.value.headers || {}) }
  headers[''] = ''
  updateConfig('headers', headers)
}

function removeHeader(key: string) {
  const headers = { ...(config.value.headers || {}) }
  delete headers[key]
  updateConfig('headers', headers)
}

function updateHeaderKey(oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const headers = { ...(config.value.headers || {}) }
  headers[newKey] = headers[oldKey]
  delete headers[oldKey]
  updateConfig('headers', headers)
}

function updateHeaderValue(key: string, value: string) {
  const headers = { ...(config.value.headers || {}) }
  headers[key] = value
  updateConfig('headers', headers)
}

// Transfer callback helpers
const callbackEvents = ['on_waiting', 'on_connect'] as const
type CallbackEvent = typeof callbackEvents[number]

const callbackLabels: Record<CallbackEvent, string> = {
  on_waiting: 'On Waiting',
  on_connect: 'On Connect',
}

function getCallbackConfig(event: CallbackEvent) {
  return (config.value[event] as Record<string, any>) || {}
}

function updateCallbackField(event: CallbackEvent, field: string, value: any) {
  const cb = { ...getCallbackConfig(event), [field]: value }
  updateConfig(event, cb)
}

function addCallbackHeader(event: CallbackEvent) {
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}), '': '' }
  updateCallbackField(event, 'headers', headers)
}

function removeCallbackHeader(event: CallbackEvent, key: string) {
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}) }
  delete headers[key]
  updateCallbackField(event, 'headers', headers)
}

function updateCallbackHeaderKey(event: CallbackEvent, oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}) }
  headers[newKey] = headers[oldKey]
  delete headers[oldKey]
  updateCallbackField(event, 'headers', headers)
}

function updateCallbackHeaderValue(event: CallbackEvent, key: string, value: string) {
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}) }
  headers[key] = value
  updateCallbackField(event, 'headers', headers)
}


// Dynamic variable helpers ---------------------------------------------------
const testVariableValues = ref<Record<string, string>>({})
const isTestingHTTP = ref(false)

watch(() => props.node.id, () => {
  testVariableValues.value = {}
})

const availableVariables = computed(() => props.availableVariables || [])

function templateVariables(text: string) {
  const found = new Set<string>()
  const re = /{{\s*([^{}]+?)\s*}}/g
  let match: RegExpExecArray | null
  while ((match = re.exec(text || '')) !== null) found.add(match[1].trim())
  return Array.from(found)
}

const usedTTSVariables = computed(() => templateVariables(String(config.value.greeting_text || '')))

const httpTemplateVariables = computed(() => {
  const chunks = [String(config.value.url || ''), String(config.value.body_template || '')]
  for (const value of Object.values(config.value.headers || {})) chunks.push(String(value || ''))
  const found = new Set<string>()
  for (const chunk of chunks) for (const name of templateVariables(chunk)) found.add(name)
  return Array.from(found)
})

function insertTemplateVariable(current: string, path: string) {
  const token = `{{${path}}}`
  if (!current) return token
  if (/\s$/.test(current)) return current + token
  return current + ' ' + token
}

function insertVariableIntoTTS(variable: IVRVariableDefinition) {
  const next = insertTemplateVariable(String(config.value.greeting_text || ''), variable.path)
  updateConfigEntries({ greeting_text: next, audio_file: undefined })
}

function insertVariableIntoHTTPURL(variable: IVRVariableDefinition) {
  updateConfig('url', insertTemplateVariable(String(config.value.url || ''), variable.path))
}

function insertVariableIntoHTTPBody(variable: IVRVariableDefinition) {
  updateConfig('body_template', insertTemplateVariable(String(config.value.body_template || ''), variable.path))
}

function substituteTestVariables(text: string) {
  return String(text || '').replace(/{{\s*([^{}]+?)\s*}}/g, (_full, name: string) => testVariableValues.value[name.trim()] ?? '')
}

function friendlyVariableLabel(path: string) {
  const leaf = path.split('.').pop() || path
  return leaf
    .replace(/_/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/\b\w/g, c => c.toUpperCase())
}

const discoveredHTTPVariables = computed<IVRVariableDefinition[]>(() => {
  const storeAs = String(config.value.response_store_as || '').trim()
  if (!storeAs) return []
  const schema = Array.isArray(config.value.response_schema) ? config.value.response_schema : []
  return schema
    .filter((item: IVRHTTPDiscoveredVariable) => !!item?.path)
    .map((item: IVRHTTPDiscoveredVariable) => ({
      path: `${storeAs}.${item.path}`,
      label: friendlyVariableLabel(item.path),
      source: props.node.label || 'HTTP Response',
      type: item.type,
    }))
})

async function testAndDiscoverHTTPVariables() {
  const requestURL = String(config.value.url || '').trim()
  const storeAs = String(config.value.response_store_as || '').trim()
  if (!requestURL) {
    toast.error('Enter an HTTP callback URL first')
    return
  }
  if (!storeAs) {
    toast.error('Set “Store Response As” before discovering variables')
    return
  }
  for (const name of httpTemplateVariables.value) {
    if (!String(testVariableValues.value[name] || '').trim()) {
      toast.error(`Enter a test value for ${name}`)
      return
    }
  }

  isTestingHTTP.value = true
  try {
    const headers: Record<string, string> = {}
    for (const [key, value] of Object.entries(config.value.headers || {})) {
      if (String(key).trim()) headers[String(key)] = substituteTestVariables(String(value || ''))
    }
    const res = await ivrFlowsService.testHTTP({
      url: substituteTestVariables(requestURL),
      method: String(config.value.method || 'GET'),
      headers,
      body: substituteTestVariables(String(config.value.body_template || '')),
      timeout_seconds: Number(config.value.timeout_seconds || 10),
    })
    const payload = (res.data as any)?.data || res.data
    if (!payload?.is_json) {
      toast.error(`HTTP ${payload?.status_code ?? ''}: response is not JSON`)
      return
    }
    const schema = Array.isArray(payload.variables) ? payload.variables : []
    updateConfig('response_schema', schema)
    toast.success(`HTTP ${payload.status_code}: discovered ${schema.length} variables`)
  } catch (e: any) {
    toast.error(e?.response?.data?.message || e?.message || 'HTTP test failed')
  } finally {
    isTestingHTTP.value = false
  }
}

// Goto flow targets
const gotoFlowTargets = computed(() =>
  callingStore.ivrFlows.filter(f => f.id !== props.currentFlowId)
)

// Audio section used by greeting, menu, gather, hangup
const audioNodeTypes: IVRNodeType[] = ['greeting', 'menu', 'gather', 'hangup']
const hasAudio = computed(() => audioNodeTypes.includes(props.node.type))

const greetingTab = computed(() =>
  config.value.greeting_text ? 'text' : 'audio'
)
</script>

<template>
  <div class="space-y-4 p-4">
    <div class="flex items-center justify-between">
      <h3 class="font-semibold text-sm capitalize">{{ node.type.replace('_', ' ') }}</h3>
      <Button variant="ghost" size="icon" class="h-7 w-7" @click="emit('delete')">
        <Trash2 class="h-3.5 w-3.5 text-destructive" />
      </Button>
    </div>

    <!-- Label -->
    <div class="space-y-1.5">
      <Label class="text-xs">Label</Label>
      <Input :model-value="node.label" @update:model-value="updateLabel" class="h-8 text-sm" />
    </div>

    <!-- Audio Section (greeting, menu, gather, hangup) -->
    <div v-if="hasAudio" class="space-y-1.5">
      <Label class="text-xs">Audio</Label>
      <Tabs :default-value="greetingTab">
        <TabsList class="h-8">
          <TabsTrigger value="audio" class="text-xs h-7 px-2">
            <Upload class="h-3 w-3 mr-1" /> Upload
          </TabsTrigger>
          <TabsTrigger value="text" class="text-xs h-7 px-2">
            <Type class="h-3 w-3 mr-1" /> TTS
          </TabsTrigger>
        </TabsList>
        <TabsContent value="audio" class="mt-2">
          <div class="flex items-center gap-2">
            <div v-if="config.audio_file" class="flex items-center gap-1 flex-1 min-w-0 px-2 py-1 border rounded-md bg-muted/50">
              <Button variant="ghost" size="icon" class="h-5 w-5 shrink-0" @click="togglePlayback">
                <Pause v-if="isPlaying" class="h-3 w-3" />
                <Play v-else class="h-3 w-3" />
              </Button>
              <span class="text-xs truncate">{{ config.audio_file }}</span>
              <Button variant="ghost" size="icon" class="h-5 w-5 shrink-0 ml-auto" @click="removeAudio">
                <X class="h-3 w-3 text-destructive" />
              </Button>
            </div>
            <Button v-else variant="outline" size="sm" class="h-7 text-xs w-full" @click="triggerFileUpload" :disabled="isUploading">
              <Loader2 v-if="isUploading" class="h-3 w-3 mr-1 animate-spin" />
              <Upload v-else class="h-3 w-3 mr-1" />
              Upload Audio
            </Button>
            <input ref="audioFileInput" type="file" accept="audio/*" class="hidden" @change="handleFileSelect" />
          </div>
        </TabsContent>
        <TabsContent value="text" class="mt-2 space-y-2">
          <Textarea
            :model-value="config.greeting_text || ''"
            @update:model-value="(v: string) => updateConfigEntries(v ? { greeting_text: v, audio_file: undefined } : { greeting_text: v })"
            placeholder="Enter text for TTS..."
            class="min-h-[72px] text-xs resize-none"
            :maxlength="500"
          />
          <IVRVariablePicker :variables="availableVariables" @select="insertVariableIntoTTS" />
          <div class="rounded-lg border p-2.5 space-y-2 bg-muted/20">
            <div class="text-[11px] font-medium">TTS Options <span class="text-muted-foreground font-normal">(optional overrides)</span></div>
            <div class="space-y-1">
              <Label class="text-[10px]">Provider</Label>
              <Select :model-value="config.tts_provider || '__global__'" @update:model-value="(v: any) => updateConfig('tts_provider', v === '__global__' ? '' : v)">
                <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="__global__">Global default · {{ ttsDefaults?.default_provider || 'local' }}</SelectItem><SelectItem value="local">Local Piper</SelectItem><SelectItem value="google_cloud">Google Cloud TTS</SelectItem><SelectItem value="gemini">Gemini TTS</SelectItem></SelectContent>
              </Select>
            </div>
            <div class="grid grid-cols-2 gap-2">
              <div v-if="effectiveTTSProvider === 'local'" class="space-y-1">
                <Label class="text-[10px]">Voice model</Label>
                <Select :model-value="config.tts_model || '__global__'" @update:model-value="(v: any) => updateConfig('tts_model', v === '__global__' ? '' : v)">
                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__global__">Global default{{ ttsDefaults?.default_model ? ` · ${ttsDefaults.default_model}` : '' }}</SelectItem>
                    <SelectItem v-for="model in ttsModels" :key="model.file" :value="model.file">{{ model.name }}{{ model.language ? ` · ${model.language}` : '' }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="space-y-1">
                <Label class="text-[10px]">Number pronunciation</Label>
                <Select :model-value="config.tts_number_mode || '__global__'" @update:model-value="(v: any) => updateConfig('tts_number_mode', v === '__global__' ? '' : v)">
                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__global__">Global default</SelectItem>
                    <SelectItem value="natural">Natural numbers</SelectItem>
                    <SelectItem value="phone_digits">Phone numbers digit-by-digit</SelectItem>
                    <SelectItem value="all_digits">All numbers digit-by-digit</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div class="grid grid-cols-2 gap-2">
              <div v-if="effectiveTTSProvider === 'local'" class="space-y-1">
                <Label class="text-[10px]">Language / locale</Label>
                <Select :model-value="config.tts_language || '__global__'" @update:model-value="(v: any) => updateConfig('tts_language', v === '__global__' ? '' : v)">
                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__global__">Global default</SelectItem><SelectItem value="auto">Auto</SelectItem><SelectItem value="en">English</SelectItem><SelectItem value="si">Sinhala</SelectItem><SelectItem value="ta">Tamil</SelectItem><SelectItem value="hi">Hindi</SelectItem><SelectItem value="es">Spanish</SelectItem><SelectItem value="de">German</SelectItem><SelectItem value="ja">Japanese</SelectItem><SelectItem value="ko">Korean</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div v-if="effectiveTTSProvider === 'local'" class="space-y-1">
                <Label class="text-[10px]">Length scale</Label>
                <Input type="number" min="0.5" max="2" step="0.05" :model-value="String(config.tts_length_scale || '')" @update:model-value="(v: string) => updateConfig('tts_length_scale', v ? Number(v) : 0)" :placeholder="ttsDefaults ? `Global ${ttsDefaults.length_scale}` : 'Global'" class="h-7 text-xs" />
              </div>
            </div>
            <div v-if="effectiveTTSProvider === 'google_cloud'" class="space-y-2 border-t pt-2">
              <div class="space-y-1">
                <Label class="text-[10px]">Google Cloud voice</Label>
                <Select :model-value="config.tts_google_cloud_voice || '__global__'" @update:model-value="(v: any) => updateConfig('tts_google_cloud_voice', v === '__global__' ? '' : v)">
                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                  <SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.google_cloud_voice || 'not selected' }}</SelectItem><SelectItem v-for="voice in filteredNodeCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }}</SelectItem></SelectContent>
                </Select>
              </div>
              <div class="grid grid-cols-2 gap-2">
                <div class="space-y-1"><Label class="text-[10px]">Language</Label><Select :model-value="config.tts_google_cloud_language || '__global__'" @update:model-value="(v: any) => updateConfigEntries({ tts_google_cloud_language: v === '__global__' ? '' : v, tts_google_cloud_voice: '' })"><SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.google_cloud_language || 'en-US' }}</SelectItem><SelectItem v-for="item in primaryTTSLanguages" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent></Select></div>
                <div class="space-y-1"><Label class="text-[10px]">Speaking rate</Label><Input type="number" min="0.25" max="4" step="0.05" :model-value="config.tts_google_cloud_speaking_rate || 1" @update:model-value="(v: string | number) => updateConfig('tts_google_cloud_speaking_rate', Number(v) || 1)" class="h-7 text-xs" /></div>
              </div>
              <p v-if="effectiveCloudLanguage === 'si-LK'" class="text-[10px] text-primary">Google Cloud TTS currently has no published Sinhala synthesis voice. Use Local Piper or Gemini TTS for Sinhala.</p>
              <p v-else class="text-[10px] text-muted-foreground">Standard/WaveNet include up to 4M free characters monthly; Neural2 and Chirp 3 HD include up to 1M. Chirp 3 HD ignores speaking-rate controls.</p>
            </div>
            <div v-if="effectiveTTSProvider === 'gemini'" class="space-y-2 border-t pt-2">
              <div class="grid grid-cols-2 gap-2">
                <div class="space-y-1"><Label class="text-[10px]">Gemini model</Label><Select :model-value="config.tts_gemini_model || '__global__'" @update:model-value="(v: any) => updateConfig('tts_gemini_model', v === '__global__' ? '' : v)"><SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.gemini_model || 'Gemini Flash TTS' }}</SelectItem><SelectItem v-for="item in geminiModels" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent></Select></div>
                <div class="space-y-1"><Label class="text-[10px]">Gemini voice</Label><Select :model-value="config.tts_gemini_voice || '__global__'" @update:model-value="(v: any) => updateConfig('tts_gemini_voice', v === '__global__' ? '' : v)"><SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.gemini_voice || 'Kore' }}</SelectItem><SelectItem v-for="voice in geminiVoices" :key="voice" :value="voice">{{ voice }}</SelectItem></SelectContent></Select></div>
              </div>
              <div class="space-y-1"><Label class="text-[10px]">Voice direction</Label><Textarea :model-value="config.tts_gemini_prompt || ''" @update:model-value="(v: string) => updateConfig('tts_gemini_prompt', v)" placeholder="Warm, professional, brisk pace, natural Sri Lankan accent…" class="min-h-[58px] text-xs" /></div>
              <p class="text-[10px] text-muted-foreground">Gemini automatically handles supported languages from the text. The voice direction controls style, accent, pace and tone.</p>
            </div>
            <p class="text-[10px] text-muted-foreground">Phone digit mode turns 94741682210 into 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0 before the selected provider speaks it.</p>
          </div>
          <div v-if="usedTTSVariables.length" class="flex flex-wrap gap-1">
            <span
              v-for="variable in usedTTSVariables"
              :key="variable"
              class="inline-flex items-center rounded-full border border-primary/30 bg-primary/10 px-2 py-0.5 text-[10px] font-medium text-primary"
            >
              {{ friendlyVariableLabel(variable) }}
              <code class="ml-1 opacity-60">{{ variable }}</code>
            </span>
          </div>
        </TabsContent>
      </Tabs>
    </div>

    <!-- Greeting: interruptible -->
    <div v-if="node.type === 'greeting'" class="flex items-center gap-2">
      <Switch :checked="!!config.interruptible" @update:checked="(v: boolean) => updateConfig('interruptible', v)" />
      <Label class="text-xs">Interruptible by DTMF</Label>
    </div>

    <!-- Menu: options -->
    <template v-if="node.type === 'menu'">
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Max Retries</Label>
        <Input type="number" :model-value="String(config.max_retries || 3)" @update:model-value="(v: string) => updateConfig('max_retries', parseInt(v) || 3)" class="h-8 text-sm" min="1" max="10" />
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Menu Options</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addMenuOption">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <div v-for="(opt, digit) in (config.options || {})" :key="String(digit)" class="flex items-center gap-1.5">
          <span class="font-mono text-xs font-bold w-5 text-center">{{ digit }}</span>
          <Input :model-value="(opt as any)?.label || ''" @update:model-value="(v: string) => updateMenuOption(String(digit), 'label', v)" placeholder="Label" class="h-7 text-xs flex-1" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeMenuOption(String(digit))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
    </template>

    <!-- Gather: config -->
    <template v-if="node.type === 'gather'">
      <div class="space-y-1.5">
        <Label class="text-xs">Max Digits</Label>
        <Input type="number" :model-value="String(config.max_digits || 10)" @update:model-value="(v: string) => updateConfig('max_digits', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="20" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Terminator</Label>
        <Input :model-value="config.terminator || '#'" @update:model-value="(v: string) => updateConfig('terminator', v)" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store As (variable name)</Label>
        <Input :model-value="config.store_as || ''" @update:model-value="(v: string) => updateConfig('store_as', v)" placeholder="e.g. account_number" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Max Retries</Label>
        <Input type="number" :model-value="String(config.max_retries || 3)" @update:model-value="(v: string) => updateConfig('max_retries', parseInt(v) || 3)" class="h-8 text-sm" min="1" max="10" />
      </div>
    </template>

    <!-- HTTP Callback: config -->
    <template v-if="node.type === 'http_callback'">
      <div class="space-y-1.5">
        <Label class="text-xs">URL</Label>
        <Input :model-value="config.url || ''" @update:model-value="(v: string) => updateConfig('url', v)" placeholder="https://api.example.com/ivr" class="h-8 text-xs font-mono" />
        <IVRVariablePicker :variables="availableVariables" @select="insertVariableIntoHTTPURL" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Method</Label>
        <Select :model-value="config.method || 'GET'" @update:model-value="(v: any) => updateConfig('method', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="GET">GET</SelectItem>
            <SelectItem value="POST">POST</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Headers</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addHeader">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <div v-for="(val, key) in (config.headers || {})" :key="String(key)" class="flex items-center gap-1">
          <Input :model-value="String(key)" @update:model-value="(v: string) => updateHeaderKey(String(key), v)" placeholder="Key" class="h-7 text-xs flex-1" />
          <Input :model-value="String(val)" @update:model-value="(v: string) => updateHeaderValue(String(key), v)" placeholder="Value" class="h-7 text-xs flex-1" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeHeader(String(key))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Body Template</Label>
        <Textarea :model-value="config.body_template || ''" @update:model-value="(v: string) => updateConfig('body_template', v)" placeholder='{"phone": "{{caller_phone}}"}' class="min-h-[60px] text-xs font-mono resize-none" />
        <IVRVariablePicker :variables="availableVariables" @select="insertVariableIntoHTTPBody" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="30" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store Response As (variable name)</Label>
        <Input :model-value="config.response_store_as || ''" @update:model-value="(v: string) => updateConfig('response_store_as', v)" placeholder="e.g. api_response" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <div>
          <Label class="text-xs">Progress Audio <span class="text-muted-foreground font-normal">(optional)</span></Label>
          <p class="text-[10px] text-muted-foreground mt-0.5">Loops while the HTTP request is processing and stops as soon as the response arrives.</p>
        </div>
        <div class="flex items-center gap-2">
          <div v-if="config.progress_audio_file" class="flex items-center gap-1 flex-1 min-w-0 px-2 py-1 border rounded-md bg-muted/50">
            <Button type="button" variant="ghost" size="icon" class="h-5 w-5 shrink-0" @click="toggleProgressAudioPlayback">
              <Pause v-if="isPlayingProgressAudio" class="h-3 w-3" />
              <Play v-else class="h-3 w-3" />
            </Button>
            <span class="text-xs truncate">{{ config.progress_audio_file }}</span>
            <Button type="button" variant="ghost" size="icon" class="h-5 w-5 shrink-0 ml-auto" @click="removeProgressAudio">
              <X class="h-3 w-3 text-destructive" />
            </Button>
          </div>
          <Button v-else type="button" variant="outline" size="sm" class="h-7 text-xs w-full" @click="triggerProgressAudioUpload" :disabled="isUploadingProgressAudio">
            <Loader2 v-if="isUploadingProgressAudio" class="h-3 w-3 animate-spin" />
            <Upload v-else class="h-3 w-3" />
            Select Progress Audio
          </Button>
          <input ref="progressAudioFileInput" type="file" accept="audio/*" class="hidden" @change="handleProgressAudioFileSelect" />
        </div>
      </div>
      <div class="space-y-2 rounded-lg border p-2.5 bg-muted/20">
        <div>
          <div class="text-xs font-medium">Discover Response Variables</div>
          <p class="text-[10px] text-muted-foreground mt-0.5">Send a safe test request and turn JSON fields into visual flow variables. Test values are not saved.</p>
        </div>
        <div v-if="httpTemplateVariables.length" class="space-y-1.5">
          <div v-for="name in httpTemplateVariables" :key="name" class="space-y-1">
            <Label class="text-[10px]">Test value for <code>{{ name }}</code></Label>
            <Input
              :model-value="testVariableValues[name] || ''"
              @update:model-value="(v: string) => testVariableValues[name] = v"
              :placeholder="`Temporary ${name}`"
              class="h-7 text-xs"
            />
          </div>
        </div>
        <Button type="button" variant="outline" size="sm" class="h-7 text-xs w-full" :disabled="isTestingHTTP" @click="testAndDiscoverHTTPVariables">
          <Loader2 v-if="isTestingHTTP" class="h-3 w-3 animate-spin" />
          <FlaskConical v-else class="h-3 w-3" />
          {{ isTestingHTTP ? 'Testing…' : 'Test & Discover Variables' }}
        </Button>
        <div v-if="discoveredHTTPVariables.length" class="space-y-1.5">
          <div class="text-[10px] text-muted-foreground">{{ discoveredHTTPVariables.length }} discovered variables</div>
          <div class="flex flex-wrap gap-1 max-h-36 overflow-y-auto">
            <span
              v-for="variable in discoveredHTTPVariables"
              :key="variable.path"
              class="inline-flex max-w-full items-center rounded-full border bg-background px-2 py-0.5 text-[10px]"
              :title="variable.path"
            >
              <span class="font-medium truncate">{{ variable.label }}</span>
              <code class="ml-1 text-muted-foreground truncate">{{ variable.path }}</code>
            </span>
          </div>
        </div>
      </div>
    </template>

    <!-- Transfer: team selector -->
    <template v-if="node.type === 'transfer'">
      <div class="space-y-1.5">
        <Label class="text-xs">Team</Label>
        <Select :model-value="config.team_id || 'none'" @update:model-value="(v: any) => updateConfig('team_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select team" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select team...</SelectItem>
            <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">
              {{ team.name }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <!-- HTTP Callbacks per lifecycle event -->
      <div class="space-y-2 mt-3">
        <Label class="text-xs font-medium">HTTP Callbacks</Label>
        <p class="text-[10px] text-muted-foreground">Configure API calls to your CRM at each transfer stage.</p>

        <div v-for="event in callbackEvents" :key="event" class="border rounded-md">
          <button class="w-full flex items-center justify-between px-3 py-1.5 text-xs font-medium hover:bg-muted/50" @click="updateCallbackField(event, '_expanded', !getCallbackConfig(event)._expanded)">
            <span>{{ callbackLabels[event] }}</span>
            <span v-if="getCallbackConfig(event).url" class="text-[10px] text-pink-500">Configured</span>
          </button>

          <div v-if="getCallbackConfig(event)._expanded" class="px-3 pb-3 space-y-1.5 border-t">
            <div class="space-y-1 pt-2">
              <Label class="text-[10px]">URL</Label>
              <Input :model-value="getCallbackConfig(event).url || ''" @update:model-value="(v: string) => updateCallbackField(event, 'url', v)" placeholder="https://crm.example.com/api/call" class="h-7 text-xs font-mono" />
            </div>
            <div class="space-y-1">
              <Label class="text-[10px]">Method</Label>
              <Select :model-value="getCallbackConfig(event).method || 'POST'" @update:model-value="(v: any) => updateCallbackField(event, 'method', v)">
                <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="GET">GET</SelectItem>
                  <SelectItem value="POST">POST</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-1">
              <div class="flex items-center justify-between">
                <Label class="text-[10px]">Headers</Label>
                <Button variant="outline" size="sm" class="h-5 text-[10px] px-1.5" @click="addCallbackHeader(event)">
                  <Plus class="h-2.5 w-2.5 mr-0.5" /> Add
                </Button>
              </div>
              <div v-for="(val, key) in (getCallbackConfig(event).headers || {})" :key="String(key)" class="flex items-center gap-1">
                <Input :model-value="String(key)" @update:model-value="(v: string) => updateCallbackHeaderKey(event, String(key), v)" placeholder="Key" class="h-6 text-[10px] flex-1" />
                <Input :model-value="String(val)" @update:model-value="(v: string) => updateCallbackHeaderValue(event, String(key), v)" placeholder="Value" class="h-6 text-[10px] flex-1" />
                <Button variant="ghost" size="icon" class="h-5 w-5" @click="removeCallbackHeader(event, String(key))">
                  <Trash2 class="h-2.5 w-2.5 text-destructive" />
                </Button>
              </div>
            </div>
            <div v-if="(getCallbackConfig(event).method || 'POST') === 'POST'" class="space-y-1">
              <Label class="text-[10px]">Body Template</Label>
              <Textarea :model-value="getCallbackConfig(event).body_template || ''" @update:model-value="(v: string) => updateCallbackField(event, 'body_template', v)" :placeholder='`{"phone": "{{caller_phone}}", "transfer_id": "{{transfer_id}}"}`' class="min-h-[50px] text-[10px] font-mono resize-none" />
            </div>
          </div>
        </div>
      </div>

      <!-- Available variables reference -->
      <div class="border rounded-md mt-2">
        <button class="w-full flex items-center justify-between px-3 py-1.5 text-xs font-medium hover:bg-muted/50" @click="updateConfig('_vars_expanded', !config._vars_expanded)">
          <span>Available Variables</span>
        </button>
        <div v-if="config._vars_expanded" class="px-3 pb-2 border-t">
          <div class="flex flex-wrap gap-1 pt-2">
            <code v-for="v in ['caller_phone', 'contact_id', 'call_log_id', 'transfer_id', 'team_id', 'whatsapp_account', 'status', 'transferred_at', 'ivr_path', 'agent_id *', 'agent_email *', 'agent_name *', 'hold_duration **', 'talk_duration **']" :key="v" class="bg-muted px-1.5 py-0.5 rounded text-[10px] cursor-pointer hover:bg-muted/80" :title="v.includes('*') ? 'Available on connect/complete only' : ''">{{ v.replace(' *', '').replace(' **', '') }}</code>
          </div>
          <p class="text-[9px] text-muted-foreground mt-1.5">* on_connect/on_complete only &nbsp; ** on_complete only</p>
        </div>
      </div>
    </template>

    <!-- Goto Flow: flow selector -->
    <template v-if="node.type === 'goto_flow'">
      <div class="space-y-1.5">
        <Label class="text-xs">Target Flow</Label>
        <Select :model-value="config.flow_id || 'none'" @update:model-value="(v: any) => updateConfig('flow_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select flow" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select flow...</SelectItem>
            <SelectItem v-for="flow in gotoFlowTargets" :key="flow.id" :value="flow.id">
              {{ flow.name }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
    </template>

    <!-- Timing: schedule -->
    <template v-if="node.type === 'timing'">
      <div class="space-y-1.5">
        <Label class="text-xs">Schedule</Label>
        <div v-for="(entry, idx) in schedule" :key="idx" class="flex items-center gap-1.5 text-xs">
          <span class="w-12 capitalize">{{ entry.day.slice(0, 3) }}</span>
          <Switch :checked="entry.enabled" @update:checked="(v: boolean) => updateScheduleEntry(Number(idx), 'enabled', v)" />
          <Input
            v-if="entry.enabled"
            type="time"
            :model-value="entry.start_time"
            @update:model-value="(v: string) => updateScheduleEntry(Number(idx), 'start_time', v)"
            class="h-8 text-xs w-28"
          />
          <span v-if="entry.enabled" class="text-muted-foreground">-</span>
          <Input
            v-if="entry.enabled"
            type="time"
            :model-value="entry.end_time"
            @update:model-value="(v: string) => updateScheduleEntry(Number(idx), 'end_time', v)"
            class="h-8 text-xs w-28"
          />
        </div>
      </div>
    </template>
  </div>
</template>
