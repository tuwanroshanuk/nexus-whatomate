from pathlib import Path


def replace_once(path: str, old: str, new: str):
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f"Expected snippet not found in {path}: {old[:120]!r}")
    p.write_text(s.replace(old, new, 1))

# Backend model removal: only managed ModelDir models, never active default.
piper = Path("internal/tts/piper.go")
s = piper.read_text()
marker = "func (p *PiperTTS) resolveModel(model string) (string, error) {"
if marker not in s:
    raise SystemExit("resolveModel marker not found")
method = r'''// UninstallModel removes an installed Piper model and its matching JSON config.
// Only files inside the managed ModelDir can be removed. The active default
// model must be changed before it can be uninstalled.
func (p *PiperTTS) UninstallModel(name string) (int64, error) {
	filename, err := sanitizeModelFilename(name)
	if err != nil {
		return 0, err
	}
	settings := p.GetSettings()
	if filename == settings.DefaultModel {
		return 0, fmt.Errorf("cannot uninstall the active default model; select another default model first")
	}

	dir := p.modelDir()
	modelPath := filepath.Join(dir, filename)
	if !fileExists(modelPath) {
		// A legacy configured model may appear in ListModels even when it lives
		// outside ModelDir. Never delete arbitrary external paths from this API.
		if p.ModelPath != "" && filepath.Base(p.ModelPath) == filename && fileExists(p.ModelPath) {
			return 0, fmt.Errorf("this model is configured outside the managed model directory and cannot be uninstalled here")
		}
		return 0, fmt.Errorf("Piper model %q is not installed in the managed model directory", filename)
	}

	var freed int64
	if info, statErr := os.Stat(modelPath); statErr == nil {
		freed += info.Size()
	}
	configPath := modelPath + ".json"
	if info, statErr := os.Stat(configPath); statErr == nil {
		freed += info.Size()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.Remove(modelPath); err != nil {
		return 0, fmt.Errorf("remove model: %w", err)
	}
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return freed, fmt.Errorf("model removed but config cleanup failed: %w", err)
	}
	return freed, nil
}

'''
s = s.replace(marker, method + marker, 1)
piper.write_text(s)

# HTTP handler.
handler = Path("internal/handlers/tts_settings.go")
s = handler.read_text()
marker = "type ttsPreviewRequest struct {"
if marker not in s:
    raise SystemExit("ttsPreviewRequest marker not found")
uninstall = r'''// UninstallTTSModel removes a managed Piper model and its matching config file.
func (a *App) UninstallTTSModel(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	name := strings.TrimSpace(r.RequestCtx.UserValue("name").(string))
	if name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Model name is required", nil, "")
	}
	freed, err := a.TTS.UninstallModel(name)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	a.Log.Info("TTS model uninstalled", "model", name, "freed_bytes", freed)
	return r.SendEnvelope(map[string]any{
		"model":       name,
		"freed_bytes": freed,
	})
}

'''
s = s.replace(marker, uninstall + marker, 1)
handler.write_text(s)

# Route.
replace_once(
    "cmd/whatomate/main.go",
    '\tg.POST("/api/tts/models/download", app.DownloadTTSModel)\n\tg.POST("/api/tts/preview", app.PreviewTTS)',
    '\tg.POST("/api/tts/models/download", app.DownloadTTSModel)\n\tg.DELETE("/api/tts/models/{name}", app.UninstallTTSModel)\n\tg.POST("/api/tts/preview", app.PreviewTTS)',
)

# Frontend service.
replace_once(
    "frontend/src/services/api.ts",
    "  downloadModel: (data: { name: string; model_url: string; config_url?: string }) =>\n    api.post<TTSModelInfo>('/tts/models/download', data, { timeout: 5 * 60 * 1000 }),\n  preview:",
    "  downloadModel: (data: { name: string; model_url: string; config_url?: string }) =>\n    api.post<TTSModelInfo>('/tts/models/download', data, { timeout: 5 * 60 * 1000 }),\n  uninstallModel: (name: string) =>\n    api.delete<{ model: string; freed_bytes: number }>(`/tts/models/${encodeURIComponent(name)}`),\n  preview:",
)

# Settings UI: trash icon, state/action, and uninstall button.
ui = Path("frontend/src/components/settings/TTSSettingsPanel.vue")
s = ui.read_text()
s = s.replace(
    "import { Download, Loader2, Play, Pause, RefreshCw, Volume2 } from 'lucide-vue-next'",
    "import { Download, Loader2, Play, Pause, RefreshCw, Trash2, Volume2 } from 'lucide-vue-next'",
    1,
)
s = s.replace(
    "const previewing = ref(false)\nconst models = ref<TTSModelInfo[]>([])",
    "const previewing = ref(false)\nconst uninstalling = ref('')\nconst models = ref<TTSModelInfo[]>([])",
    1,
)
needle = "function stopPreview() {"
if needle not in s:
    raise SystemExit("stopPreview marker not found")
action = r'''async function uninstallModel(model: TTSModelInfo) {
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

'''
s = s.replace(needle, action + needle, 1)
old = '''            <code class="text-[10px] text-muted-foreground truncate ml-3">{{ model.file }}</code>'''
new = '''            <div class="flex items-center gap-2 ml-3 shrink-0">
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
            </div>'''
if old not in s:
    raise SystemExit("installed model code element not found")
s = s.replace(old, new, 1)
ui.write_text(s)
