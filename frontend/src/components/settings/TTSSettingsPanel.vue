<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ttsService, ivrFlowsService, type TTSModelInfo, type TTSSettings } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Download, Loader2, Play, Pause, RefreshCw, Volume2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const loading = ref(true)
const saving = ref(false)
const downloading = ref(false)
const previewing = ref(false)
const models = ref<TTSModelInfo[]>([])
const modelDir = ref('')
const settings = ref<TTSSettings>({ default_model: '', default_language: 'auto', number_mode: 'phone_digits', length_scale: 1 })
const download = ref({ name: '', model_url: '', config_url: '' })
const previewText = ref('Your phone number is 94741682210.')
let previewAudio: HTMLAudioElement | null = null

const languages = [
  ['auto', 'Auto / model language'], ['en', 'English'], ['si', 'Sinhala'], ['ta', 'Tamil'], ['hi', 'Hindi'],
  ['es', 'Spanish'], ['de', 'German'], ['fr', 'French'], ['ja', 'Japanese'], ['ko', 'Korean'], ['zh', 'Chinese'],
]

const speedLabel = computed(() => settings.value.length_scale < .9 ? 'Faster' : settings.value.length_scale > 1.1 ? 'Slower' : 'Normal')

async function load() {
  loading.value = true
  try {
    const res = await ttsService.getSettings()
    const data = (res.data as any)?.data || res.data
    settings.value = data.settings
    models.value = data.models || []
    modelDir.value = data.model_dir || ''
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

function stopPreview() {
  if (previewAudio) { previewAudio.pause(); previewAudio.currentTime = 0; previewAudio = null }
  previewing.value = false
}

async function preview() {
  if (previewing.value) { stopPreview(); return }
  try {
    const res = await ttsService.preview({
      text: previewText.value,
      model: settings.value.default_model,
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
          <div class="grid md:grid-cols-2 gap-4">
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
            <code class="text-[10px] text-muted-foreground truncate ml-3">{{ model.file }}</code>
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
