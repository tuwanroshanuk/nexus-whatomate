<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ttsService, ivrFlowsService, type TTSModelInfo, type TTSSettings, type TTSProviderStatus, type GoogleCloudTTSVoice } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Download, Loader2, Play, Pause, RefreshCw, Trash2, Volume2, Cloud, Cpu, KeyRound, FlaskConical } from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const loading = ref(true)
const saving = ref(false)
const downloading = ref(false)
const previewing = ref(false)
const savingGeminiKey = ref(false)
const testingGemini = ref(false)
const geminiApiKey = ref('')
const providers = ref<TTSProviderStatus>({ local: false, gemini: { configured: false }, google_cloud: { configured: false } })
const savingCloudCredentials = ref(false)
const testingCloud = ref(false)
const loadingCloudVoices = ref(false)
const googleCloudServiceAccount = ref('')
const googleCloudVoices = ref<GoogleCloudTTSVoice[]>([])
const uninstalling = ref('')
const models = ref<TTSModelInfo[]>([])
const modelDir = ref('')
const settings = ref<TTSSettings>({ default_provider: 'local', default_model: '', default_language: 'auto', number_mode: 'phone_digits', length_scale: 1, gemini_model: 'gemini-2.5-flash-preview-tts', gemini_voice: 'Kore', gemini_prompt: '', google_cloud_voice: '', google_cloud_language: 'en-US' })
const download = ref({ name: '', model_url: '', config_url: '' })
const previewText = ref('Your phone number is 94741682210.')
let previewAudio: HTMLAudioElement | null = null

const geminiModels = [['gemini-3.1-flash-tts-preview', 'Gemini 3.1 Flash TTS Preview'], ['gemini-2.5-flash-preview-tts', 'Gemini 2.5 Flash Preview TTS'], ['gemini-2.5-pro-preview-tts', 'Gemini 2.5 Pro Preview TTS']]
const geminiVoices = ['Kore','Puck','Charon','Zephyr','Fenrir','Leda','Orus','Aoede','Callirrhoe','Autonoe','Enceladus','Iapetus','Umbriel','Algieba','Despina','Erinome','Algenib','Rasalgethi','Laomedeia','Achernar','Alnilam','Schedar','Gacrux','Pulcherrima','Achird','Zubenelgenubi','Vindemiatrix','Sadachbia','Sadaltager','Sulafat']

const languages = [
  ['auto', 'Auto / model language'],
  ['si-LK', 'Sinhala (Sri Lanka)'],
  ['en-US', 'English (United States)'],
  ['ko-KR', 'Korean (South Korea)'],
  ['es-ES', 'Spanish (Spain)'],
  ['ja-JP', 'Japanese (Japan)'],
]

const primaryCloudLanguages = [
  ['si-LK', 'Sinhala (Sri Lanka)'],
  ['en-US', 'English (United States)'],
  ['ko-KR', 'Korean (South Korea)'],
  ['es-ES', 'Spanish (Spain)'],
  ['ja-JP', 'Japanese (Japan)'],
]
const filteredGoogleCloudVoices = computed(() => {
  const code = settings.value.google_cloud_language
  if (!code || code === 'si-LK') return []
  return googleCloudVoices.value.filter(v => v.language_codes?.includes(code))
})
const cloudSinhalaUnavailable = computed(() => settings.value.google_cloud_language === 'si-LK')

const cloudTierLabel = (tier: string) => ({ standard: 'Standard · 4M free/mo', wavenet: 'WaveNet · 4M free/mo', neural2: 'Neural2 · 1M free/mo', chirp3_hd: 'Chirp 3 HD · 1M free/mo', studio: 'Studio · 1M free/mo', other: 'Other' } as Record<string,string>)[tier] || tier
const selectedCloudVoice = computed(() => googleCloudVoices.value.find(v => v.name === settings.value.google_cloud_voice))

const speedLabel = computed(() => settings.value.length_scale < .9 ? 'Faster' : settings.value.length_scale > 1.1 ? 'Slower' : 'Normal')

async function load() {
  loading.value = true
  try {
    const res = await ttsService.getSettings()
    const data = (res.data as any)?.data || res.data
    settings.value = data.settings
    models.value = data.models || []
    modelDir.value = data.model_dir || ''
    providers.value = data.providers || providers.value
    if (providers.value.google_cloud?.configured && !googleCloudVoices.value.length) void loadGoogleCloudVoices()
  } catch (e: any) {
    toast.error(e?.response?.data?.message || 'Could not load TTS settings')
  } finally { loading.value = false }
}

async function save() {
  saving.value = true
  try {
    await ttsService.updateSettings(settings.value)
    toast.success('TTS defaults saved')
    await load()
  } catch (e: any) {
    toast.error(e?.response?.data?.message || 'Could not save TTS settings')
  } finally { saving.value = false }
}

async function downloadModel() {
  if (!download.value.name.trim() || !download.value.model_url.trim()) {
    toast.error('Model name and HTTPS model URL are required')
    return
  }
  downloading.value = true
  try {
    await ttsService.downloadModel(download.value)
    toast.success('Piper model downloaded and installed')
    download.value = { name: '', model_url: '', config_url: '' }
    await load()
  } catch (e: any) {
    toast.error(e?.response?.data?.message || 'Model download failed')
  } finally { downloading.value = false }
}

async function uninstallModel(model: TTSModelInfo) {
  if (model.is_default) {
    toast.error('Select another default voice and save it before uninstalling this model')
    return
  }
  if (!window.confirm(`Uninstall ${model.name}? The ONNX model and its config file will be permanently removed from disk.`)) return
  uninstalling.value = model.file
  try {
    const res = await ttsService.uninstallModel(model.file)
    const data = (res.data as any)?.data || res.data
    toast.success(`${model.name} uninstalled · ${size(Number(data?.freed_bytes || 0))} reclaimed`)
    await load()
  } catch (e: any) {
    toast.error(e?.response?.data?.message || 'Could not uninstall TTS model')
  } finally {
    uninstalling.value = ''
  }
}


async function loadGoogleCloudVoices() {
  if (!providers.value.google_cloud?.configured) return
  loadingCloudVoices.value = true
  try {
    const res = await ttsService.getGoogleCloudVoices()
    const data = (res.data as any)?.data || res.data
    googleCloudVoices.value = data.voices || []
  } catch (e: any) { toast.error(e?.response?.data?.message || 'Could not load Google Cloud voices') }
  finally { loadingCloudVoices.value = false }
}

async function saveGoogleCloudCredentials() {
  if (!googleCloudServiceAccount.value.trim()) { toast.error('Paste the service-account JSON credential'); return }
  savingCloudCredentials.value = true
  try {
    const parsed = JSON.parse(googleCloudServiceAccount.value)
    await ttsService.setGoogleCloudCredentials(JSON.stringify(parsed))
    googleCloudServiceAccount.value = ''
    providers.value.google_cloud.configured = true
    providers.value.google_cloud.project_id = parsed.project_id || ''
    toast.success('Google Cloud TTS credentials encrypted and saved')
    await loadGoogleCloudVoices()
  } catch (e: any) {
    if (e instanceof SyntaxError) toast.error('Service-account JSON is invalid')
    else toast.error(e?.response?.data?.message || 'Could not save Google Cloud credentials')
  } finally { savingCloudCredentials.value = false }
}

async function clearGoogleCloudCredentials() {
  if (!confirm('Remove the saved Google Cloud TTS credentials? Cloud TTS nodes will stop working.')) return
  try {
    await ttsService.clearGoogleCloudCredentials()
    providers.value.google_cloud = { configured: false }
    googleCloudVoices.value = []
    toast.success('Google Cloud TTS credentials removed')
  } catch (e: any) { toast.error(e?.response?.data?.message || 'Could not remove Google Cloud credentials') }
}

async function testGoogleCloud() {
  testingCloud.value = true
  try {
    const res = await ttsService.testGoogleCloud()
    const data = (res.data as any)?.data || res.data
    if (data?.filename) await new Audio(ivrFlowsService.getAudioUrl(data.filename)).play()
    toast.success('Google Cloud TTS connection succeeded')
  } catch (e: any) { toast.error(e?.response?.data?.message || 'Google Cloud TTS test failed') }
  finally { testingCloud.value = false }
}

async function saveGeminiCredentials() {
  if (!geminiApiKey.value.trim()) { toast.error('Enter a Gemini API key'); return }
  savingGeminiKey.value = true
  try {
    await ttsService.setGeminiCredentials(geminiApiKey.value.trim())
    geminiApiKey.value = ''
    providers.value.gemini.configured = true
    toast.success('Gemini TTS credential encrypted and saved')
  } catch (e: any) { toast.error(e?.response?.data?.message || 'Could not save Gemini credential') }
  finally { savingGeminiKey.value = false }
}

async function clearGeminiCredentials() {
  if (!confirm('Remove the saved Gemini TTS credential? Gemini nodes will stop working until another key is saved.')) return
  try {
    await ttsService.clearGeminiCredentials()
    providers.value.gemini.configured = false
    toast.success('Gemini TTS credential removed')
  } catch (e: any) { toast.error(e?.response?.data?.message || 'Could not remove Gemini credential') }
}

async function testGemini() {
  testingGemini.value = true
  try {
    const res = await ttsService.testGemini()
    const data = (res.data as any)?.data || res.data
    if (data?.filename) {
      const audio = new Audio(ivrFlowsService.getAudioUrl(data.filename))
      await audio.play()
    }
    toast.success('Gemini TTS connection succeeded')
  } catch (e: any) { toast.error(e?.response?.data?.message || 'Gemini TTS test failed') }
  finally { testingGemini.value = false }
}

function stopPreview() {
  if (previewAudio) { previewAudio.pause(); previewAudio.currentTime = 0; previewAudio = null }
  previewing.value = false
}

async function preview() {
  if (previewing.value) { stopPreview(); return }
  try {
    const res = await ttsService.preview({
      text: previewText.value,
      provider: settings.value.default_provider,
      model: settings.value.default_model,
      gemini_model: settings.value.gemini_model,
      gemini_voice: settings.value.gemini_voice,
      gemini_prompt: settings.value.gemini_prompt,
      google_cloud_voice: settings.value.google_cloud_voice,
      google_cloud_language: settings.value.google_cloud_language,
      language: settings.value.default_language,
      number_mode: settings.value.number_mode,
      length_scale: settings.value.length_scale,
    })
    const data = (res.data as any)?.data || res.data
    const audio = new Audio(ivrFlowsService.getAudioUrl(data.filename))
    previewAudio = audio
    previewing.value = true
    audio.onended = stopPreview
    audio.onerror = stopPreview
    await audio.play()
  } catch (e: any) {
    stopPreview()
    toast.error(e?.response?.data?.message || 'TTS preview failed')
  }
}

function size(bytes: number) {
  if (!bytes) return '—'
  if (bytes > 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024).toFixed(0)} KB`
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <div v-if="loading" class="flex justify-center py-12"><Loader2 class="h-6 w-6 animate-spin" /></div>
    <template v-else>
      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3">
          <div class="flex items-center justify-between">
            <div>
              <h3 class="text-lg font-semibold text-white light:text-gray-900">Text-to-Speech Defaults</h3>
              <p class="text-sm text-white/40 light:text-gray-500">Defaults apply to every IVR TTS node unless that node overrides them.</p>
            </div>
            <Button variant="ghost" size="icon" @click="load"><RefreshCw class="h-4 w-4" /></Button>
          </div>
        </div>
        <div class="p-6 pt-3 space-y-4">
          <div class="space-y-2">
            <Label>Default TTS provider</Label>
            <Select v-model="settings.default_provider">
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="local"><span class="inline-flex items-center gap-2"><Cpu class="h-3.5 w-3.5" />Local Piper</span></SelectItem>
                <SelectItem value="google_cloud" :disabled="!providers.google_cloud.configured"><span class="inline-flex items-center gap-2"><Cloud class="h-3.5 w-3.5" />Google Cloud TTS{{ providers.google_cloud.configured ? '' : ' · add credentials below' }}</span></SelectItem>
                <SelectItem value="gemini" :disabled="!providers.gemini.configured"><span class="inline-flex items-center gap-2"><Cloud class="h-3.5 w-3.5" />Gemini TTS{{ providers.gemini.configured ? '' : ' · add key below' }}</span></SelectItem>
              </SelectContent>
            </Select>
            <p class="text-xs text-muted-foreground">This is the default for all IVR TTS nodes. Every node can override it independently.</p>
          </div>

          <div v-if="settings.default_provider === 'local'" class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Default Piper model</Label>
              <Select v-model="settings.default_model">
                <SelectTrigger><SelectValue placeholder="Select installed model" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="model in models" :key="model.file" :value="model.file">{{ model.name }}{{ model.language ? ` · ${model.language}` : '' }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-2">
              <Label>Language / locale</Label>
              <Select v-model="settings.default_language">
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem v-for="item in languages" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent>
              </Select>
              <p class="text-xs text-white/40 light:text-gray-500">The selected Piper voice determines the spoken language. This locale is retained for formatting and future language-aware rules.</p>
            </div>
          </div>

          <div v-if="settings.default_provider === 'google_cloud'" class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Google Cloud voice</Label>
              <Select v-model="settings.google_cloud_voice" :disabled="cloudSinhalaUnavailable">
                <SelectTrigger><SelectValue placeholder="Select a Cloud TTS voice" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="voice in filteredGoogleCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }} · {{ cloudTierLabel(voice.tier) }}</SelectItem>
                </SelectContent>
              </Select>
              <Button variant="outline" size="sm" :disabled="loadingCloudVoices" @click="loadGoogleCloudVoices"><Loader2 v-if="loadingCloudVoices" class="h-4 w-4 animate-spin" /><RefreshCw v-else class="h-4 w-4" />Refresh voices</Button>
            </div>
            <div class="space-y-2">
              <Label>Language</Label>
              <Select v-model="settings.google_cloud_language" @update:model-value="() => { settings.google_cloud_voice = '' }">
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem v-for="item in primaryCloudLanguages" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent>
              </Select>
              <p v-if="cloudSinhalaUnavailable" class="text-xs text-primary">Cloud Text-to-Speech currently does not publish a Sinhala si-LK synthesis voice. Use Local Piper for Sinhala, or Gemini TTS when cloud neural Sinhala is required.</p>
              <p v-else-if="selectedCloudVoice" class="text-xs text-muted-foreground">{{ cloudTierLabel(selectedCloudVoice.tier) }} · {{ selectedCloudVoice.gender }} · {{ selectedCloudVoice.language_codes.join(', ') }}</p>
            </div>
          </div>

          <div class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Number pronunciation</Label>
              <Select v-model="settings.number_mode">
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="natural">Natural numbers</SelectItem>
                  <SelectItem value="phone_digits">Phone numbers digit-by-digit</SelectItem>
                  <SelectItem value="all_digits">All numbers digit-by-digit</SelectItem>
                </SelectContent>
              </Select>
              <p class="text-xs text-white/40 light:text-gray-500">Phone mode expands only 7–15 digit sequences. Example: 94741682210 → 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0.</p>
            </div>
            <div class="space-y-2">
              <Label>Speech speed · {{ speedLabel }}</Label>
              <Input type="number" step="0.05" min="0.5" max="2" v-model.number="settings.length_scale" />
              <p class="text-xs text-white/40 light:text-gray-500">Piper length scale: lower is faster, higher is slower. Default 1.0.</p>
            </div>
          </div>

          <div class="space-y-2">
            <Label>Preview</Label>
            <Textarea v-model="previewText" class="min-h-[70px]" maxlength="500" />
            <div class="flex justify-between gap-2">
              <Button variant="outline" size="sm" @click="preview"><Pause v-if="previewing" class="h-4 w-4" /><Play v-else class="h-4 w-4" />{{ previewing ? 'Stop' : 'Preview voice' }}</Button>
              <Button size="sm" :disabled="saving" @click="save"><Loader2 v-if="saving" class="h-4 w-4 animate-spin" /><Volume2 v-else class="h-4 w-4" />Save TTS defaults</Button>
            </div>
          </div>
        </div>
      </div>

      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3 flex items-start justify-between gap-3">
          <div>
            <div class="flex items-center gap-2"><Cloud class="h-5 w-5 text-primary" /><h3 class="text-lg font-semibold text-white light:text-gray-900">External TTS · Google Cloud</h3></div>
            <p class="text-sm text-white/40 light:text-gray-500 mt-1">Use Legacy Standard/WaveNet, Neural2, Studio and Chirp 3 HD voices through Cloud Text-to-Speech.</p>
          </div>
          <span class="text-xs px-2 py-1 border" :class="providers.google_cloud.configured ? 'text-primary border-primary/30 bg-primary/10' : 'text-muted-foreground'">{{ providers.google_cloud.configured ? `Connected · ${providers.google_cloud.project_id || 'Google Cloud'}` : 'Not configured' }}</span>
        </div>
        <div class="p-6 pt-3 space-y-4">
          <div class="grid md:grid-cols-4 gap-2 text-xs">
            <div class="border p-3"><strong>Standard</strong><div class="text-muted-foreground mt-1">4M chars/month free</div></div>
            <div class="border p-3"><strong>WaveNet</strong><div class="text-muted-foreground mt-1">4M chars/month free</div></div>
            <div class="border p-3"><strong>Neural2</strong><div class="text-muted-foreground mt-1">1M chars/month free</div></div>
            <div class="border p-3"><strong>Chirp 3 HD</strong><div class="text-muted-foreground mt-1">1M chars/month free</div></div>
          </div>
          <p class="text-xs text-muted-foreground">Google Cloud requires billing to be enabled even when your usage remains inside its monthly free quota. Charges apply automatically after the free limit.</p>
          <div class="space-y-2">
            <Label>Service-account JSON credential</Label>
            <Textarea v-model="googleCloudServiceAccount" class="min-h-[110px] font-mono text-xs" autocomplete="off" :placeholder="providers.google_cloud.configured ? 'Credential saved securely · paste another service-account JSON to replace it' : '{ &quot;type&quot;: &quot;service_account&quot;, ... }'" />
            <p class="text-xs text-muted-foreground">Encrypted on the server with app.encryption_key and never returned to the browser after saving. Grant only the permissions required for Cloud Text-to-Speech.</p>
          </div>
          <div class="flex flex-wrap justify-between gap-2">
            <div class="flex gap-2">
              <Button :disabled="savingCloudCredentials || !googleCloudServiceAccount.trim()" @click="saveGoogleCloudCredentials"><Loader2 v-if="savingCloudCredentials" class="h-4 w-4 animate-spin" /><KeyRound v-else class="h-4 w-4" />Save credentials</Button>
              <Button variant="outline" :disabled="!providers.google_cloud.configured || testingCloud" @click="testGoogleCloud"><Loader2 v-if="testingCloud" class="h-4 w-4 animate-spin" /><FlaskConical v-else class="h-4 w-4" />Test</Button>
            </div>
            <Button v-if="providers.google_cloud.configured" variant="ghost" class="text-destructive" @click="clearGoogleCloudCredentials"><Trash2 class="h-4 w-4" />Remove credentials</Button>
          </div>
        </div>
      </div>

      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3 flex items-start justify-between gap-3">
          <div>
            <div class="flex items-center gap-2"><Cloud class="h-5 w-5 text-primary" /><h3 class="text-lg font-semibold text-white light:text-gray-900">External TTS · Gemini</h3></div>
            <p class="text-sm text-white/40 light:text-gray-500 mt-1">Gemini API TTS with automatic multilingual speech, neural voices and natural-language style control. Gemini TTS has no free usage tier.</p>
          </div>
          <span class="text-xs px-2 py-1 border" :class="providers.gemini.configured ? 'text-primary border-primary/30 bg-primary/10' : 'text-muted-foreground'">{{ providers.gemini.configured ? 'Configured' : 'Not configured' }}</span>
        </div>
        <div class="p-6 pt-3 space-y-4">
          <div class="space-y-2">
            <Label>Gemini API key</Label>
            <div class="flex gap-2">
              <Input v-model="geminiApiKey" type="password" autocomplete="new-password" :placeholder="providers.gemini.configured ? 'Saved securely · enter a new key to replace' : 'AIza…'" />
              <Button :disabled="savingGeminiKey || !geminiApiKey.trim()" @click="saveGeminiCredentials"><Loader2 v-if="savingGeminiKey" class="h-4 w-4 animate-spin" /><KeyRound v-else class="h-4 w-4" />Save key</Button>
            </div>
            <p class="text-xs text-muted-foreground">The key is encrypted on the server with app.encryption_key, stored with 0600 permissions, and is never returned to the browser after saving.</p>
          </div>
          <div class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2"><Label>Default Gemini model</Label><Select v-model="settings.gemini_model"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="item in geminiModels" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent></Select></div>
            <div class="space-y-2"><Label>Default Gemini voice</Label><Select v-model="settings.gemini_voice"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="voice in geminiVoices" :key="voice" :value="voice">{{ voice }}</SelectItem></SelectContent></Select></div>
          </div>
          <div class="space-y-2"><Label>Default voice direction <span class="text-muted-foreground">(optional)</span></Label><Textarea v-model="settings.gemini_prompt" placeholder="Example: Speak warmly, clearly and professionally with a natural Sri Lankan accent." class="min-h-[70px]" /></div>
          <div class="flex flex-wrap justify-between gap-2">
            <Button variant="outline" :disabled="!providers.gemini.configured || testingGemini" @click="testGemini"><Loader2 v-if="testingGemini" class="h-4 w-4 animate-spin" /><FlaskConical v-else class="h-4 w-4" />Test connection</Button>
            <Button v-if="providers.gemini.configured" variant="ghost" class="text-destructive" @click="clearGeminiCredentials"><Trash2 class="h-4 w-4" />Remove credentials</Button>
          </div>
        </div>
      </div>

      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3">
          <h3 class="text-lg font-semibold text-white light:text-gray-900">Installed Piper Models</h3>
          <p class="text-sm text-white/40 light:text-gray-500">{{ models.length }} model{{ models.length === 1 ? '' : 's' }} · {{ modelDir || 'configured model directory' }}</p>
        </div>
        <div class="p-6 pt-3 space-y-2">
          <div v-for="model in models" :key="model.file" class="flex items-center justify-between border rounded-lg px-3 py-2">
            <div class="min-w-0">
              <div class="text-sm font-medium truncate">{{ model.name }} <span v-if="model.is_default" class="text-xs text-primary">Default</span></div>
              <div class="text-xs text-muted-foreground">{{ model.language || 'Language not declared' }} · {{ size(model.size) }} · {{ model.has_config ? 'config ready' : 'no .onnx.json config' }}</div>
            </div>
            <div class="flex items-center gap-2 ml-3 shrink-0">
              <code class="text-[10px] text-muted-foreground max-w-[180px] truncate">{{ model.file }}</code>
              <Button
                variant="ghost"
                size="icon"
                class="h-8 w-8"
                :disabled="model.is_default || uninstalling === model.file"
                :title="model.is_default ? 'Change the default model before uninstalling' : 'Uninstall model and free disk space'"
                @click="uninstallModel(model)"
              >
                <Loader2 v-if="uninstalling === model.file" class="h-4 w-4 animate-spin" />
                <Trash2 v-else class="h-4 w-4 text-destructive" />
              </Button>
            </div>
          </div>
          <p v-if="!models.length" class="text-sm text-muted-foreground">No models were found. Download one below or configure piper_model/model_dir.</p>
        </div>
      </div>

      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3">
          <h3 class="text-lg font-semibold text-white light:text-gray-900">Download Piper Model</h3>
          <p class="text-sm text-white/40 light:text-gray-500">Install an ONNX voice from HTTPS. Piper voices are commonly hosted on Hugging Face. Downloaded files are saved in the configured model directory.</p>
        </div>
        <div class="p-6 pt-3 space-y-3">
          <div class="grid md:grid-cols-2 gap-3">
            <div class="space-y-1"><Label>Model name</Label><Input v-model="download.name" placeholder="en_US-lessac-medium.onnx" /></div>
            <div class="space-y-1"><Label>Model URL</Label><Input v-model="download.model_url" placeholder="https://.../voice.onnx" /></div>
          </div>
          <div class="space-y-1"><Label>Config URL <span class="text-muted-foreground">(recommended)</span></Label><Input v-model="download.config_url" placeholder="https://.../voice.onnx.json" /></div>
          <div class="flex justify-end"><Button size="sm" :disabled="downloading" @click="downloadModel"><Loader2 v-if="downloading" class="h-4 w-4 animate-spin" /><Download v-else class="h-4 w-4" />{{ downloading ? 'Downloading…' : 'Download & install' }}</Button></div>
        </div>
      </div>
    </template>
  </div>
</template>
