from pathlib import Path


def replace_once(path, old, new):
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f"missing expected snippet in {path}: {old[:120]!r}")
    p.write_text(s.replace(old, new, 1))

# Backend wiring ----------------------------------------------------------------
replace_once(
    "cmd/whatomate/main.go",
    '''\t\tapp.TTS = &tts.PiperTTS{\n\t\t\tBinaryPath:    cfg.TTS.PiperBinary,\n\t\t\tModelPath:     cfg.TTS.PiperModel,\n\t\t\tOpusencBinary: cfg.TTS.OpusencBinary,\n\t\t\tAudioDir:      cfg.Calling.AudioDir,\n\t\t}''',
    '''\t\tapp.TTS = &tts.PiperTTS{\n\t\t\tBinaryPath:    cfg.TTS.PiperBinary,\n\t\t\tModelPath:     cfg.TTS.PiperModel,\n\t\t\tModelDir:      cfg.TTS.ModelDir,\n\t\t\tSettingsPath:  cfg.TTS.SettingsPath,\n\t\t\tOpusencBinary: cfg.TTS.OpusencBinary,\n\t\t\tAudioDir:      cfg.Calling.AudioDir,\n\t\t}'''
)
replace_once(
    "cmd/whatomate/main.go",
    '''\t// IVR Flows\n\tg.GET("/api/ivr-flows", app.ListIVRFlows)''',
    '''\t// TTS settings and Piper voice management\n\tg.GET("/api/tts/settings", app.GetTTSSettings)\n\tg.PUT("/api/tts/settings", app.UpdateTTSSettings)\n\tg.POST("/api/tts/models/download", app.DownloadTTSModel)\n\tg.POST("/api/tts/preview", app.PreviewTTS)\n\n\t// IVR Flows\n\tg.GET("/api/ivr-flows", app.ListIVRFlows)'''
)
replace_once(
    "internal/handlers/ivr_flows.go",
    '''\t\tfilename, err := a.TTS.Generate(greetingText)''',
    '''\t\tfilename, err := a.TTS.GenerateWithConfig(greetingText, config)'''
)
replace_once(
    "config.example.toml",
    '''# piper_model = "/opt/piper/models/en_US-lessac-medium.onnx"\n# opusenc_binary = "opusenc"  # defaults to finding in PATH''',
    '''# piper_model = "/opt/piper/models/en_US-lessac-medium.onnx"\n# model_dir = "/opt/piper/models"       # mount this directory persistently to keep downloaded voices\n# settings_path = "/opt/piper/models/.whatomate-tts-settings.json"\n# opusenc_binary = "opusenc"  # defaults to finding in PATH'''
)

# Backend tests -----------------------------------------------------------------
Path("internal/tts/piper_test.go").write_text(r'''package tts

import "testing"

func TestPrepareTextPhoneDigits(t *testing.T) {
    got := PrepareText("Call 94741682210 now, order 123.", NumberModePhoneDigits)
    want := "Call 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0 now, order 123."
    if got != want { t.Fatalf("got %q want %q", got, want) }
}

func TestPrepareTextAllDigits(t *testing.T) {
    got := PrepareText("PIN 1234", NumberModeAllDigits)
    want := "PIN 1, 2, 3, 4"
    if got != want { t.Fatalf("got %q want %q", got, want) }
}

func TestPrepareTextNatural(t *testing.T) {
    input := "94741682210 and 42"
    if got := PrepareText(input, NumberModeNatural); got != input { t.Fatalf("got %q", got) }
}
''')

# Frontend API ------------------------------------------------------------------
replace_once(
    "frontend/src/services/api.ts",
    '''export const ivrFlowsService = {''',
    '''export interface TTSSettings {\n  default_model: string\n  default_language: string\n  number_mode: 'natural' | 'phone_digits' | 'all_digits'\n  length_scale: number\n}\n\nexport interface TTSModelInfo {\n  file: string\n  name: string\n  language: string\n  size: number\n  has_config: boolean\n  is_default: boolean\n}\n\nexport const ttsService = {\n  getSettings: () => api.get<{ settings: TTSSettings; models: TTSModelInfo[]; model_dir: string }>('/tts/settings'),\n  updateSettings: (settings: TTSSettings) => api.put<TTSSettings>('/tts/settings', settings),\n  downloadModel: (data: { name: string; model_url: string; config_url?: string }) =>\n    api.post<TTSModelInfo>('/tts/models/download', data, { timeout: 5 * 60 * 1000 }),\n  preview: (data: { text: string; model?: string; language?: string; number_mode?: string; length_scale?: number }) =>\n    api.post<{ filename: string }>('/tts/preview', data, { timeout: 90 * 1000 }),\n}\n\nexport const ivrFlowsService = {'''
)

# Settings panel ----------------------------------------------------------------
Path("frontend/src/components/settings/TTSSettingsPanel.vue").write_text(r'''<script setup lang="ts">
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
''')

# Settings page tab -------------------------------------------------------------
replace_once(
    "frontend/src/views/settings/SettingsView.vue",
    '''import LanguageSwitcher from '@/components/LanguageSwitcher.vue' ''',
    '''import LanguageSwitcher from '@/components/LanguageSwitcher.vue'\nimport TTSSettingsPanel from '@/components/settings/TTSSettingsPanel.vue' '''
)
replace_once(
    "frontend/src/views/settings/SettingsView.vue",
    'TabsList class="grid w-full grid-cols-3 mb-6',
    'TabsList class="grid w-full grid-cols-4 mb-6'
)
replace_once(
    "frontend/src/views/settings/SettingsView.vue",
    '''            <TabsTrigger value="calling" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">\n              <Phone class="h-4 w-4 mr-2" />\n              {{ $t('settings.calling') }}\n            </TabsTrigger>''',
    '''            <TabsTrigger value="calling" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">\n              <Phone class="h-4 w-4 mr-2" />\n              {{ $t('settings.calling') }}\n            </TabsTrigger>\n            <TabsTrigger value="tts" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">\n              <Volume2 class="h-4 w-4 mr-2" />\n              TTS\n            </TabsTrigger>'''
)
replace_once(
    "frontend/src/views/settings/SettingsView.vue",
    '''          </TabsContent>\n        </Tabs>\n      </div>\n    </ScrollArea>''',
    '''          </TabsContent>\n\n          <TabsContent value="tts">\n            <TTSSettingsPanel />\n          </TabsContent>\n        </Tabs>\n      </div>\n    </ScrollArea>'''
)

# IVR per-node options -----------------------------------------------------------
replace_once(
    "frontend/src/components/calling/IVRNodeProperties.vue",
    '''import { computed, ref, watch } from 'vue' ''',
    '''import { computed, onMounted, ref, watch } from 'vue' '''
)
replace_once(
    "frontend/src/components/calling/IVRNodeProperties.vue",
    '''import { ivrFlowsService } from '@/services/api' ''',
    '''import { ivrFlowsService, ttsService, type TTSModelInfo, type TTSSettings } from '@/services/api' '''
)
replace_once(
    "frontend/src/components/calling/IVRNodeProperties.vue",
    '''const config = computed(() => props.node.config || {})''',
    '''const config = computed(() => props.node.config || {})\n\nconst ttsModels = ref<TTSModelInfo[]>([])\nconst ttsDefaults = ref<TTSSettings | null>(null)\n\nasync function loadTTSOptions() {\n  try {\n    const res = await ttsService.getSettings()\n    const data = (res.data as any)?.data || res.data\n    ttsModels.value = data.models || []\n    ttsDefaults.value = data.settings || null\n  } catch {\n    // TTS can be disabled server-side; keep the normal audio editor usable.\n  }\n}\n\nonMounted(loadTTSOptions)'''
)
replace_once(
    "frontend/src/components/calling/IVRNodeProperties.vue",
    '''          <IVRVariablePicker :variables="availableVariables" @select="insertVariableIntoTTS" />''',
    '''          <IVRVariablePicker :variables="availableVariables" @select="insertVariableIntoTTS" />\n          <div class="rounded-lg border p-2.5 space-y-2 bg-muted/20">\n            <div class="text-[11px] font-medium">TTS Options <span class="text-muted-foreground font-normal">(optional overrides)</span></div>\n            <div class="grid grid-cols-2 gap-2">\n              <div class="space-y-1">\n                <Label class="text-[10px]">Voice model</Label>\n                <Select :model-value="config.tts_model || '__global__'" @update:model-value="(v: any) => updateConfig('tts_model', v === '__global__' ? '' : v)">\n                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>\n                  <SelectContent>\n                    <SelectItem value="__global__">Global default{{ ttsDefaults?.default_model ? ` · ${ttsDefaults.default_model}` : '' }}</SelectItem>\n                    <SelectItem v-for="model in ttsModels" :key="model.file" :value="model.file">{{ model.name }}{{ model.language ? ` · ${model.language}` : '' }}</SelectItem>\n                  </SelectContent>\n                </Select>\n              </div>\n              <div class="space-y-1">\n                <Label class="text-[10px]">Number pronunciation</Label>\n                <Select :model-value="config.tts_number_mode || '__global__'" @update:model-value="(v: any) => updateConfig('tts_number_mode', v === '__global__' ? '' : v)">\n                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>\n                  <SelectContent>\n                    <SelectItem value="__global__">Global default</SelectItem>\n                    <SelectItem value="natural">Natural numbers</SelectItem>\n                    <SelectItem value="phone_digits">Phone numbers digit-by-digit</SelectItem>\n                    <SelectItem value="all_digits">All numbers digit-by-digit</SelectItem>\n                  </SelectContent>\n                </Select>\n              </div>\n            </div>\n            <div class="grid grid-cols-2 gap-2">\n              <div class="space-y-1">\n                <Label class="text-[10px]">Language / locale</Label>\n                <Select :model-value="config.tts_language || '__global__'" @update:model-value="(v: any) => updateConfig('tts_language', v === '__global__' ? '' : v)">\n                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>\n                  <SelectContent>\n                    <SelectItem value="__global__">Global default</SelectItem><SelectItem value="auto">Auto</SelectItem><SelectItem value="en">English</SelectItem><SelectItem value="si">Sinhala</SelectItem><SelectItem value="ta">Tamil</SelectItem><SelectItem value="hi">Hindi</SelectItem><SelectItem value="es">Spanish</SelectItem><SelectItem value="de">German</SelectItem><SelectItem value="ja">Japanese</SelectItem><SelectItem value="ko">Korean</SelectItem>\n                  </SelectContent>\n                </Select>\n              </div>\n              <div class="space-y-1">\n                <Label class="text-[10px]">Length scale</Label>\n                <Input type="number" min="0.5" max="2" step="0.05" :model-value="String(config.tts_length_scale || '')" @update:model-value="(v: string) => updateConfig('tts_length_scale', v ? Number(v) : 0)" :placeholder="ttsDefaults ? `Global ${ttsDefaults.length_scale}` : 'Global'" class="h-7 text-xs" />\n              </div>\n            </div>\n            <p class="text-[10px] text-muted-foreground">Phone digit mode turns 94741682210 into 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0 before Piper speaks it.</p>\n          </div>'''
)

# remove one existing unused import that breaks the repo's strict typecheck
replace_once(
    "frontend/src/views/settings/SettingsView.vue",
    '''import { getSelectedRingtone, setSelectedRingtone, RINGTONE_STORAGE_KEY, DEFAULT_RINGTONE } from '@/services/websocket' ''',
    '''import { setSelectedRingtone, RINGTONE_STORAGE_KEY, DEFAULT_RINGTONE } from '@/services/websocket' '''
)

# Remove the patch artifacts from the final implementation commit.
Path('.github/scripts/apply_tts_settings.py').unlink(missing_ok=True)
Path('.github/workflows/apply-tts-settings.yml').unlink(missing_ok=True)
