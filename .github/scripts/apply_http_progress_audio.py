from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise SystemExit(f"Expected snippet not found in {path}: {old[:160]!r}")
    p.write_text(text.replace(old, new, 1))


# Runtime: pass the active RTP player into the HTTP node.
replace_once(
    "internal/calling/ivr.go",
    "\t\tcase IVRNodeHTTPCallback:\n\t\t\toutcome = m.executeHTTPCallback(session, node, ctx)\n",
    "\t\tcase IVRNodeHTTPCallback:\n\t\t\toutcome = m.executeHTTPCallback(session, node, ctx, player)\n",
)

# Runtime: start optional progress audio before the blocking HTTP request, loop it,
# then stop/reset the player as soon as the request finishes.
replace_once(
    "internal/calling/ivr.go",
    "func (m *Manager) executeHTTPCallback(session *CallSession, node *IVRNode, ctx *IVRContext) string {\n\turl, _ := node.Config[\"url\"].(string)\n",
    "func (m *Manager) executeHTTPCallback(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {\n\turl, _ := node.Config[\"url\"].(string)\n",
)

replace_once(
    "internal/calling/ivr.go",
    "\t// Interpolate URL and body\n\turl = interpolateTemplate(url, ctx.Variables)\n\tbody := interpolateTemplate(bodyTemplate, ctx.Variables)\n\n\tresult, err := executeHTTPCallback(url, method, headers, body, time.Duration(timeoutSecs)*time.Second)\n",
    "\t// Interpolate URL and body\n\turl = interpolateTemplate(url, ctx.Variables)\n\tbody := interpolateTemplate(bodyTemplate, ctx.Variables)\n\n\t// Optional progress audio keeps the caller informed while the HTTP request\n\t// is in flight. It loops until the request returns (success or failure), then\n\t// the shared RTP player is stopped and reset before the next IVR node runs.\n\tprogressAudioFile, _ := node.Config[\"progress_audio_file\"].(string)\n\tvar progressDone chan struct{}\n\tif progressAudioFile != \"\" && player != nil && m.config.AudioDir != \"\" {\n\t\tfullPath := filepath.Join(m.config.AudioDir, progressAudioFile)\n\t\tprogressDone = make(chan struct{})\n\t\tgo func() {\n\t\t\tdefer close(progressDone)\n\t\t\tif err := player.PlayFileLoop(fullPath); err != nil {\n\t\t\t\tm.log.Error(\"Failed to play HTTP progress audio\", \"error\", err, \"call_id\", session.ID, \"file\", progressAudioFile)\n\t\t\t}\n\t\t}()\n\t}\n\n\tresult, err := executeHTTPCallback(url, method, headers, body, time.Duration(timeoutSecs)*time.Second)\n\tif progressDone != nil {\n\t\tplayer.Stop()\n\t\t<-progressDone\n\t\tplayer.ResetAfterInterrupt()\n\t}\n",
)

# Frontend: default config includes optional progress audio.
replace_once(
    "frontend/src/views/calling/IVRFlowEditorView.vue",
    "    http_callback: { url: '', method: 'GET', headers: {}, body_template: '', timeout_seconds: 10 },\n",
    "    http_callback: { url: '', method: 'GET', headers: {}, body_template: '', timeout_seconds: 10, progress_audio_file: '' },\n",
)

# Frontend: add progress-audio upload/preview state and handlers.
insert_after = """function stopAudio() {
  if (audioElement.value) {
    audioElement.value.pause()
    audioElement.value = null
  }
  isPlaying.value = false
}
"""
progress_helpers = r'''

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
'''
replace_once(
    "frontend/src/components/calling/IVRNodeProperties.vue",
    insert_after,
    insert_after + progress_helpers,
)

# Frontend: insert optional Progress Audio section after Store Response As.
store_response_block = '''      <div class="space-y-1.5">
        <Label class="text-xs">Store Response As (variable name)</Label>
        <Input :model-value="config.response_store_as || ''" @update:model-value="(v: string) => updateConfig('response_store_as', v)" placeholder="e.g. api_response" class="h-8 text-sm" />
      </div>
'''
progress_ui = r'''      <div class="space-y-1.5">
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
'''
replace_once(
    "frontend/src/components/calling/IVRNodeProperties.vue",
    store_response_block,
    store_response_block + progress_ui,
)
