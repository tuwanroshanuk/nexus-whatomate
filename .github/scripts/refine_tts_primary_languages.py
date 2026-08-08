from pathlib import Path

# Fix imports that differ because the current Settings panel already has model-uninstall UI.
p = Path('frontend/src/components/settings/TTSSettingsPanel.vue')
s = p.read_text()
if "Cloud, Cpu, KeyRound" not in s:
    s = s.replace(
        "import { Download, Loader2, Play, Pause, RefreshCw, Trash2, Volume2 } from 'lucide-vue-next'",
        "import { Download, Loader2, Play, Pause, RefreshCw, Trash2, Volume2, Cloud, Cpu, KeyRound, FlaskConical } from 'lucide-vue-next'",
        1,
    )

# Primary business languages. Keep Auto, but deliberately focus the editor on
# Sinhala, English, Korean, Spanish and Japanese.
old_languages = """const languages = [
  ['auto', 'Auto / model language'], ['en', 'English'], ['si', 'Sinhala'], ['ta', 'Tamil'], ['hi', 'Hindi'],
  ['es', 'Spanish'], ['de', 'German'], ['fr', 'French'], ['ja', 'Japanese'], ['ko', 'Korean'], ['zh', 'Chinese'],
]"""
new_languages = """const languages = [
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
const cloudSinhalaUnavailable = computed(() => settings.value.google_cloud_language === 'si-LK')"""
if old_languages in s:
    s = s.replace(old_languages, new_languages, 1)

# Replace Cloud language free text with the primary-language selector and filter voices.
s = s.replace(
    '<SelectItem v-for="voice in googleCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }} · {{ cloudTierLabel(voice.tier) }}</SelectItem>',
    '<SelectItem v-for="voice in filteredGoogleCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }} · {{ cloudTierLabel(voice.tier) }}</SelectItem>',
    1,
)
s = s.replace(
    '<Label>Language code</Label>\n              <Input v-model="settings.google_cloud_language" placeholder="en-US" />\n              <p v-if="selectedCloudVoice" class="text-xs text-muted-foreground">{{ cloudTierLabel(selectedCloudVoice.tier) }} · {{ selectedCloudVoice.gender }} · {{ selectedCloudVoice.language_codes.join(\', \') }}</p>',
    '''<Label>Language</Label>
              <Select v-model="settings.google_cloud_language" @update:model-value="() => { settings.google_cloud_voice = '' }">
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem v-for="item in primaryCloudLanguages" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent>
              </Select>
              <p v-if="cloudSinhalaUnavailable" class="text-xs text-primary">Cloud Text-to-Speech currently does not publish a Sinhala si-LK synthesis voice. Use Local Piper for Sinhala, or Gemini TTS when cloud neural Sinhala is required.</p>
              <p v-else-if="selectedCloudVoice" class="text-xs text-muted-foreground">{{ cloudTierLabel(selectedCloudVoice.tier) }} · {{ selectedCloudVoice.gender }} · {{ selectedCloudVoice.language_codes.join(', ') }}</p>''',
    1,
)
# Disable/select messaging when Sinhala is chosen.
s = s.replace(
    '<Select v-model="settings.google_cloud_voice">',
    '<Select v-model="settings.google_cloud_voice" :disabled="cloudSinhalaUnavailable">',
    1,
)

p.write_text(s)

# Node editor: prioritize same five languages and filter the Google Cloud voice
# list according to the node/global Cloud language.
p = Path('frontend/src/components/calling/IVRNodeProperties.vue')
s = p.read_text()
anchor = "const geminiVoices = ['Kore'"
idx = s.find(anchor)
if idx >= 0 and 'primaryTTSLanguages' not in s:
    line_end = s.find('\n', idx)
    insertion = "\nconst primaryTTSLanguages = [['si-LK','Sinhala (Sri Lanka)'],['en-US','English (United States)'],['ko-KR','Korean (South Korea)'],['es-ES','Spanish (Spain)'],['ja-JP','Japanese (Japan)']]\nconst effectiveCloudLanguage = computed(() => String(config.value.tts_google_cloud_language || ttsDefaults.value?.google_cloud_language || 'en-US'))\nconst filteredNodeCloudVoices = computed(() => effectiveCloudLanguage.value === 'si-LK' ? [] : googleCloudVoices.value.filter(v => v.language_codes?.includes(effectiveCloudLanguage.value)))"
    s = s[:line_end] + insertion + s[line_end:]

s = s.replace(
    '<SelectItem v-for="voice in googleCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }}</SelectItem>',
    '<SelectItem v-for="voice in filteredNodeCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }}</SelectItem>',
    1,
)
s = s.replace(
    '<div class="space-y-1"><Label class="text-[10px]">Language</Label><Input :model-value="config.tts_google_cloud_language || \'\'" @update:model-value="(v: string) => updateConfig(\'tts_google_cloud_language\', v)" :placeholder="ttsDefaults?.google_cloud_language || \'en-US\'" class="h-7 text-xs" /></div>',
    '''<div class="space-y-1"><Label class="text-[10px]">Language</Label><Select :model-value="config.tts_google_cloud_language || '__global__'" @update:model-value="(v: any) => updateConfigEntries({ tts_google_cloud_language: v === '__global__' ? '' : v, tts_google_cloud_voice: '' })"><SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.google_cloud_language || 'en-US' }}</SelectItem><SelectItem v-for="item in primaryTTSLanguages" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent></Select></div>''',
    1,
)
s = s.replace(
    '<p class="text-[10px] text-muted-foreground">Standard/WaveNet include up to 4M free characters monthly; Neural2 and Chirp 3 HD include up to 1M. Chirp 3 HD ignores speaking-rate controls.</p>',
    '''<p v-if="effectiveCloudLanguage === 'si-LK'" class="text-[10px] text-primary">Google Cloud TTS currently has no published Sinhala synthesis voice. Use Local Piper or Gemini TTS for Sinhala.</p>
              <p v-else class="text-[10px] text-muted-foreground">Standard/WaveNet include up to 4M free characters monthly; Neural2 and Chirp 3 HD include up to 1M. Chirp 3 HD ignores speaking-rate controls.</p>''',
    1,
)
p.write_text(s)

print('Primary TTS language UX refined')
