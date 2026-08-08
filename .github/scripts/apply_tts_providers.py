from pathlib import Path


def replace(path, old, new):
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f'pattern not found in {path}: {old[:120]!r}')
    p.write_text(s.replace(old, new, 1))

# ---------------------------------------------------------------------------
# Backend: extend PiperTTS into a provider-aware TTS engine.
# ---------------------------------------------------------------------------
p = Path('internal/tts/piper.go')
s = p.read_text()
s = s.replace('\t"fmt"\n', '\t"fmt"\n\t"net/http"\n', 1)
s = s.replace('type Settings struct {\n\tDefaultModel    string  `json:"default_model"`\n\tDefaultLanguage string  `json:"default_language"`\n\tNumberMode      string  `json:"number_mode"`\n\tLengthScale     float64 `json:"length_scale"`\n}', '''type Settings struct {
\tDefaultProvider string  `json:"default_provider"`
\tDefaultModel    string  `json:"default_model"`
\tDefaultLanguage string  `json:"default_language"`
\tNumberMode      string  `json:"number_mode"`
\tLengthScale     float64 `json:"length_scale"`
\tGeminiModel     string  `json:"gemini_model"`
\tGeminiVoice     string  `json:"gemini_voice"`
\tGeminiPrompt    string  `json:"gemini_prompt"`
}''', 1)
s = s.replace('\tAudioDir      string\n\tmu            sync.RWMutex', '\tAudioDir      string\n\tHTTPClient    *http.Client\n\tSecretKey     string\n\tmu            sync.RWMutex', 1)
s = s.replace('return Settings{\n\t\tDefaultModel:    model,\n\t\tDefaultLanguage: "auto",\n\t\tNumberMode:      NumberModePhoneDigits,\n\t\tLengthScale:     1.0,\n\t}', '''return Settings{
\t\tDefaultProvider: "local",
\t\tDefaultModel:    model,
\t\tDefaultLanguage: "auto",
\t\tNumberMode:      NumberModePhoneDigits,
\t\tLengthScale:     1.0,
\t\tGeminiModel:     "gemini-2.5-flash-preview-tts",
\t\tGeminiVoice:     "Kore",
\t}''', 1)
s = s.replace('\tif !validNumberMode(settings.NumberMode) {', '''\tif settings.DefaultProvider != "local" && settings.DefaultProvider != "gemini" {
\t\tsettings.DefaultProvider = "local"
\t}
\tif settings.GeminiModel == "" {
\t\tsettings.GeminiModel = "gemini-2.5-flash-preview-tts"
\t}
\tif settings.GeminiVoice == "" {
\t\tsettings.GeminiVoice = "Kore"
\t}
\tif !validNumberMode(settings.NumberMode) {''', 1)
s = s.replace('func (p *PiperTTS) UpdateSettings(settings Settings) (Settings, error) {\n\tif !validNumberMode(settings.NumberMode) {', '''func (p *PiperTTS) UpdateSettings(settings Settings) (Settings, error) {
\tif settings.DefaultProvider != "local" && settings.DefaultProvider != "gemini" {
\t\treturn Settings{}, fmt.Errorf("invalid TTS provider")
\t}
\tif settings.GeminiModel == "" {
\t\tsettings.GeminiModel = "gemini-2.5-flash-preview-tts"
\t}
\tif settings.GeminiVoice == "" {
\t\tsettings.GeminiVoice = "Kore"
\t}
\tif settings.DefaultProvider == "gemini" {
\t\tif configured, _ := p.GeminiConfigured(); !configured {
\t\t\treturn Settings{}, fmt.Errorf("Gemini TTS credentials are not configured")
\t\t}
\t}
\tif !validNumberMode(settings.NumberMode) {''', 1)
old = '''\tsettings := p.GetSettings()
\tmodel := configString(config, "tts_model")
\tif model == "" {
\t\tmodel = settings.DefaultModel
\t}
\tmode := configString(config, "tts_number_mode")
\tif mode == "" {
\t\tmode = settings.NumberMode
\t}
'''
new = '''\tsettings := p.GetSettings()
\tprovider := configString(config, "tts_provider")
\tif provider == "" {
\t\tprovider = settings.DefaultProvider
\t}
\tmode := configString(config, "tts_number_mode")
\tif mode == "" {
\t\tmode = settings.NumberMode
\t}
\tif provider == "gemini" {
\t\treturn p.generateGemini(PrepareText(text, mode), config, settings)
\t}
\tif provider != "local" {
\t\treturn "", fmt.Errorf("unsupported TTS provider %q", provider)
\t}
\tmodel := configString(config, "tts_model")
\tif model == "" {
\t\tmodel = settings.DefaultModel
\t}
'''
if old not in s:
    raise SystemExit('GenerateWithConfig provider insertion pattern missing')
s = s.replace(old, new, 1)
p.write_text(s)

Path('internal/tts/providers.go').write_text(r'''package tts

import (
    "bytes"
    "context"
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"
)

type ProviderStatus struct {
    Local  bool                 `json:"local"`
    Gemini GeminiProviderStatus `json:"gemini"`
}

type GeminiProviderStatus struct {
    Configured bool `json:"configured"`
}

type encryptedSecrets struct {
    Version      int    `json:"version"`
    GeminiAPIKey string `json:"gemini_api_key,omitempty"`
}

type geminiResponse struct {
    Candidates []struct {
        Content struct {
            Parts []struct {
                InlineData struct {
                    Data string `json:"data"`
                    MIME string `json:"mimeType"`
                } `json:"inlineData"`
            } `json:"parts"`
        } `json:"content"`
    } `json:"candidates"`
    Error *struct {
        Code    int    `json:"code"`
        Message string `json:"message"`
        Status  string `json:"status"`
    } `json:"error,omitempty"`
}

func (p *PiperTTS) secretsPath() string { return p.settingsPath() + ".secrets" }

func (p *PiperTTS) credentialKey() ([]byte, error) {
    if strings.TrimSpace(p.SecretKey) == "" {
        return nil, fmt.Errorf("app.encryption_key must be configured before external TTS credentials can be stored")
    }
    sum := sha256.Sum256([]byte(p.SecretKey))
    return sum[:], nil
}

func (p *PiperTTS) encryptSecret(value string) (string, error) {
    key, err := p.credentialKey()
    if err != nil { return "", err }
    block, err := aes.NewCipher(key)
    if err != nil { return "", err }
    gcm, err := cipher.NewGCM(block)
    if err != nil { return "", err }
    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil { return "", err }
    sealed := gcm.Seal(nonce, nonce, []byte(value), []byte("whatomate-tts-secret-v1"))
    return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (p *PiperTTS) decryptSecret(value string) (string, error) {
    key, err := p.credentialKey()
    if err != nil { return "", err }
    raw, err := base64.RawStdEncoding.DecodeString(value)
    if err != nil { return "", err }
    block, err := aes.NewCipher(key)
    if err != nil { return "", err }
    gcm, err := cipher.NewGCM(block)
    if err != nil { return "", err }
    if len(raw) < gcm.NonceSize() { return "", fmt.Errorf("encrypted credential is invalid") }
    nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
    plain, err := gcm.Open(nil, nonce, ciphertext, []byte("whatomate-tts-secret-v1"))
    if err != nil { return "", fmt.Errorf("could not decrypt external TTS credential") }
    return string(plain), nil
}

func (p *PiperTTS) readSecrets() (encryptedSecrets, error) {
    var out encryptedSecrets
    data, err := os.ReadFile(p.secretsPath())
    if os.IsNotExist(err) { return out, nil }
    if err != nil { return out, err }
    if err := json.Unmarshal(data, &out); err != nil { return out, err }
    return out, nil
}

func (p *PiperTTS) writeSecrets(secrets encryptedSecrets) error {
    if err := os.MkdirAll(filepath.Dir(p.secretsPath()), 0755); err != nil { return err }
    secrets.Version = 1
    data, err := json.MarshalIndent(secrets, "", "  ")
    if err != nil { return err }
    tmp := p.secretsPath() + ".tmp"
    if err := os.WriteFile(tmp, data, 0600); err != nil { return err }
    if err := os.Chmod(tmp, 0600); err != nil { _ = os.Remove(tmp); return err }
    if err := os.Rename(tmp, p.secretsPath()); err != nil { _ = os.Remove(tmp); return err }
    return nil
}

func (p *PiperTTS) GeminiConfigured() (bool, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    secrets, err := p.readSecrets()
    if err != nil { return false, err }
    return secrets.GeminiAPIKey != "", nil
}

func (p *PiperTTS) GetProviderStatus() ProviderStatus {
    configured, _ := p.GeminiConfigured()
    local := strings.TrimSpace(p.BinaryPath) != "" && (strings.TrimSpace(p.ModelPath) != "" || strings.TrimSpace(p.ModelDir) != "")
    return ProviderStatus{Local: local, Gemini: GeminiProviderStatus{Configured: configured}}
}

func (p *PiperTTS) SetGeminiAPIKey(apiKey string) error {
    apiKey = strings.TrimSpace(apiKey)
    if apiKey == "" { return fmt.Errorf("Gemini API key is required") }
    encrypted, err := p.encryptSecret(apiKey)
    if err != nil { return err }
    p.mu.Lock()
    defer p.mu.Unlock()
    secrets, err := p.readSecrets()
    if err != nil { return err }
    secrets.GeminiAPIKey = encrypted
    return p.writeSecrets(secrets)
}

func (p *PiperTTS) ClearGeminiAPIKey() error {
    p.mu.Lock()
    defer p.mu.Unlock()
    secrets, err := p.readSecrets()
    if err != nil { return err }
    secrets.GeminiAPIKey = ""
    return p.writeSecrets(secrets)
}

func (p *PiperTTS) geminiAPIKey() (string, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    secrets, err := p.readSecrets()
    if err != nil { return "", err }
    if secrets.GeminiAPIKey == "" { return "", fmt.Errorf("Gemini TTS credentials are not configured") }
    return p.decryptSecret(secrets.GeminiAPIKey)
}

func geminiPrompt(text, direction string) string {
    direction = strings.TrimSpace(direction)
    if direction == "" { return text }
    return "Synthesize speech from the transcript below. Do not speak these instructions. Voice direction: " + direction + "\n\nTranscript:\n" + text
}

func (p *PiperTTS) generateGemini(text string, config map[string]any, settings Settings) (string, error) {
    key, err := p.geminiAPIKey()
    if err != nil { return "", err }
    model := configString(config, "tts_gemini_model")
    if model == "" { model = settings.GeminiModel }
    if model == "" { model = "gemini-2.5-flash-preview-tts" }
    voice := configString(config, "tts_gemini_voice")
    if voice == "" { voice = settings.GeminiVoice }
    if voice == "" { voice = "Kore" }
    prompt := configString(config, "tts_gemini_prompt")
    if prompt == "" { prompt = settings.GeminiPrompt }

    cacheKey := "gemini\x00" + model + "\x00" + voice + "\x00" + prompt + "\x00" + text
    filename := "tts_" + sha256Short(cacheKey) + ".ogg"
    outPath := filepath.Join(p.AudioDir, filename)
    if fileExists(outPath) { return filename, nil }
    if err := os.MkdirAll(p.AudioDir, 0755); err != nil { return "", err }

    body := map[string]any{
        "contents": []any{map[string]any{"parts": []any{map[string]any{"text": geminiPrompt(text, prompt)}}}},
        "generationConfig": map[string]any{
            "responseModalities": []string{"AUDIO"},
            "speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}},
        },
    }
    payload, _ := json.Marshal(body)
    endpoint := "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent"

    client := p.HTTPClient
    if client == nil { client = &http.Client{Timeout: 90 * time.Second} }
    localClient := *client
    localClient.Timeout = 90 * time.Second

    var decoded []byte
    var lastErr error
    for attempt := 0; attempt < 2; attempt++ {
        ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
        req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
        if err != nil { cancel(); return "", err }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("x-goog-api-key", key)
        resp, err := localClient.Do(req)
        if err != nil { cancel(); lastErr = err; continue }
        responseData, readErr := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
        _ = resp.Body.Close()
        cancel()
        if readErr != nil { lastErr = readErr; continue }
        var parsed geminiResponse
        if err := json.Unmarshal(responseData, &parsed); err != nil {
            lastErr = fmt.Errorf("Gemini TTS returned an invalid response")
            continue
        }
        if resp.StatusCode >= 500 && attempt == 0 {
            lastErr = fmt.Errorf("Gemini TTS temporarily failed with HTTP %d", resp.StatusCode)
            time.Sleep(350 * time.Millisecond)
            continue
        }
        if resp.StatusCode < 200 || resp.StatusCode >= 300 {
            message := fmt.Sprintf("Gemini TTS returned HTTP %d", resp.StatusCode)
            if parsed.Error != nil && parsed.Error.Message != "" { message += ": " + parsed.Error.Message }
            return "", fmt.Errorf("%s", message)
        }
        if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 || parsed.Candidates[0].Content.Parts[0].InlineData.Data == "" {
            return "", fmt.Errorf("Gemini TTS response did not contain audio")
        }
        decoded, err = base64.StdEncoding.DecodeString(parsed.Candidates[0].Content.Parts[0].InlineData.Data)
        if err != nil { return "", fmt.Errorf("could not decode Gemini TTS audio: %w", err) }
        lastErr = nil
        break
    }
    if lastErr != nil { return "", fmt.Errorf("Gemini TTS request failed: %w", lastErr) }

    pcmPath := outPath + ".tmp.pcm"
    tmpOgg := outPath + ".tmp.ogg"
    defer func() { _ = os.Remove(pcmPath); _ = os.Remove(tmpOgg) }()
    if err := os.WriteFile(pcmPath, decoded, 0600); err != nil { return "", err }
    cmd := exec.Command("ffmpeg", "-y", "-f", "s16le", "-ar", "24000", "-ac", "1", "-i", pcmPath,
        "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k", "-application", "audio", "-vn", tmpOgg)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil { return "", fmt.Errorf("Gemini audio conversion failed: %w (stderr: %s)", err, stderr.String()) }
    if err := os.Rename(tmpOgg, outPath); err != nil { return "", err }
    return filename, nil
}
''')

# ---------------------------------------------------------------------------
# Handler API for external provider credentials; extend preview payload.
# ---------------------------------------------------------------------------
replace('internal/handlers/tts_settings.go', '"providers": a.TTS.GetProviderStatus()', '"providers": a.TTS.GetProviderStatus()') if False else None
p = Path('internal/handlers/tts_settings.go')
s = p.read_text()
s = s.replace('"model_dir": a.Config.TTS.ModelDir,\n\t})', '"model_dir": a.Config.TTS.ModelDir,\n\t\t"providers": a.TTS.GetProviderStatus(),\n\t})', 1)
s = s.replace('type ttsPreviewRequest struct {\n\tText        string  `json:"text"`', 'type ttsPreviewRequest struct {\n\tText        string  `json:"text"`\n\tProvider    string  `json:"provider"`\n\tGeminiModel string  `json:"gemini_model"`\n\tGeminiVoice string  `json:"gemini_voice"`\n\tGeminiPrompt string `json:"gemini_prompt"`', 1)
s = s.replace('config := map[string]any{\n\t\t"tts_model":', 'config := map[string]any{\n\t\t"tts_provider": req.Provider,\n\t\t"tts_gemini_model": req.GeminiModel,\n\t\t"tts_gemini_voice": req.GeminiVoice,\n\t\t"tts_gemini_prompt": req.GeminiPrompt,\n\t\t"tts_model":', 1)
p.write_text(s)

Path('internal/handlers/tts_providers.go').write_text(r'''package handlers

import (
    "strings"

    "github.com/shridarpatil/whatomate/internal/models"
    "github.com/valyala/fasthttp"
    "github.com/zerodha/fastglue"
)

type geminiCredentialRequest struct {
    APIKey string `json:"api_key"`
}

func (a *App) UpdateGeminiTTSCredentials(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    var req geminiCredentialRequest
    if err := a.decodeRequest(r, &req); err != nil { return nil }
    if strings.TrimSpace(req.APIKey) == "" {
        return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Gemini API key is required", nil, "")
    }
    if err := a.TTS.SetGeminiAPIKey(req.APIKey); err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
    }
    a.Log.Info("Gemini TTS credentials updated")
    return r.SendEnvelope(map[string]any{"configured": true})
}

func (a *App) DeleteGeminiTTSCredentials(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    settings := a.TTS.GetSettings()
    if settings.DefaultProvider == "gemini" {
        return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Switch the default TTS provider to Local Piper before removing Gemini credentials", nil, "")
    }
    if err := a.TTS.ClearGeminiAPIKey(); err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not remove Gemini TTS credentials", nil, "")
    }
    a.Log.Info("Gemini TTS credentials removed")
    return r.SendEnvelope(map[string]any{"configured": false})
}

func (a *App) TestGeminiTTSProvider(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    settings := a.TTS.GetSettings()
    filename, err := a.TTS.GenerateWithConfig("Hello. Gemini text to speech is connected successfully.", map[string]any{
        "tts_provider": "gemini",
        "tts_gemini_model": settings.GeminiModel,
        "tts_gemini_voice": settings.GeminiVoice,
    })
    if err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Gemini TTS test failed: "+err.Error(), nil, "")
    }
    return r.SendEnvelope(map[string]any{"ok": true, "filename": filename})
}
''')

# Main: instantiate provider-capable engine and routes.
p = Path('cmd/whatomate/main.go')
s = p.read_text()
old = '''\t// Initialize TTS if configured (requires piper binary + model)
\tif cfg.TTS.PiperBinary != "" && cfg.TTS.PiperModel != "" {
\t\tapp.TTS = &tts.PiperTTS{
\t\t\tBinaryPath:    cfg.TTS.PiperBinary,
\t\t\tModelPath:     cfg.TTS.PiperModel,
\t\t\tModelDir:      cfg.TTS.ModelDir,
\t\t\tSettingsPath:  cfg.TTS.SettingsPath,
\t\t\tOpusencBinary: cfg.TTS.OpusencBinary,
\t\t\tAudioDir:      cfg.Calling.AudioDir,
\t\t}
\t\tcalling.SetRuntimeTTSGenerator(app.TTS)
\t\tlo.Info("TTS initialized", "piper", cfg.TTS.PiperBinary, "model", cfg.TTS.PiperModel)
\t}
'''
new = '''\t// Initialize provider-aware TTS. Local Piper remains available when its
\t// binary/model are configured; external providers can be configured securely
\t// from Settings without replacing the local engine.
\tapp.TTS = &tts.PiperTTS{
\t\tBinaryPath:    cfg.TTS.PiperBinary,
\t\tModelPath:     cfg.TTS.PiperModel,
\t\tModelDir:      cfg.TTS.ModelDir,
\t\tSettingsPath:  cfg.TTS.SettingsPath,
\t\tOpusencBinary: cfg.TTS.OpusencBinary,
\t\tAudioDir:      cfg.Calling.AudioDir,
\t\tHTTPClient:    httpClient,
\t\tSecretKey:     cfg.App.EncryptionKey,
\t}
\tcalling.SetRuntimeTTSGenerator(app.TTS)
\tlo.Info("TTS provider engine initialized", "piper", cfg.TTS.PiperBinary, "model", cfg.TTS.PiperModel)
'''
if old not in s: raise SystemExit('main TTS init block missing')
s = s.replace(old, new, 1)
s = s.replace('g.POST("/api/tts/models/download", app.DownloadTTSModel)\n\tg.POST("/api/tts/preview", app.PreviewTTS)', 'g.POST("/api/tts/models/download", app.DownloadTTSModel)\n\tg.PUT("/api/tts/providers/gemini", app.UpdateGeminiTTSCredentials)\n\tg.DELETE("/api/tts/providers/gemini", app.DeleteGeminiTTSCredentials)\n\tg.POST("/api/tts/providers/gemini/test", app.TestGeminiTTSProvider)\n\tg.POST("/api/tts/preview", app.PreviewTTS)', 1)
p.write_text(s)

# ---------------------------------------------------------------------------
# Frontend API types and methods.
# ---------------------------------------------------------------------------
p = Path('frontend/src/services/api.ts')
s = p.read_text()
s = s.replace("export interface TTSSettings {\n  default_model: string", "export interface TTSSettings {\n  default_provider: 'local' | 'gemini'\n  default_model: string", 1)
s = s.replace("  length_scale: number\n}\n\nexport interface TTSModelInfo", "  length_scale: number\n  gemini_model: string\n  gemini_voice: string\n  gemini_prompt: string\n}\n\nexport interface TTSProviderStatus {\n  local: boolean\n  gemini: { configured: boolean }\n}\n\nexport interface TTSModelInfo", 1)
s = s.replace("getSettings: () => api.get<{ settings: TTSSettings; models: TTSModelInfo[]; model_dir: string }>('/tts/settings'),", "getSettings: () => api.get<{ settings: TTSSettings; models: TTSModelInfo[]; model_dir: string; providers: TTSProviderStatus }>('/tts/settings'),", 1)
s = s.replace("  downloadModel: (data: { name: string; model_url: string; config_url?: string }) =>", "  setGeminiCredentials: (apiKey: string) => api.put('/tts/providers/gemini', { api_key: apiKey }),\n  clearGeminiCredentials: () => api.delete('/tts/providers/gemini'),\n  testGemini: () => api.post<{ ok: boolean; filename: string }>('/tts/providers/gemini/test', {}, { timeout: 90 * 1000 }),\n  downloadModel: (data: { name: string; model_url: string; config_url?: string }) =>", 1)
s = s.replace("preview: (data: { text: string; model?: string; language?: string; number_mode?: string; length_scale?: number }) =>", "preview: (data: { text: string; provider?: string; model?: string; language?: string; number_mode?: string; length_scale?: number; gemini_model?: string; gemini_voice?: string; gemini_prompt?: string }) =>", 1)
p.write_text(s)

# ---------------------------------------------------------------------------
# Settings UI: provider selector + secure Gemini credentials and controls.
# ---------------------------------------------------------------------------
p = Path('frontend/src/components/settings/TTSSettingsPanel.vue')
s = p.read_text()
s = s.replace("type TTSModelInfo, type TTSSettings", "type TTSModelInfo, type TTSSettings, type TTSProviderStatus", 1)
s = s.replace("Download, Loader2, Play, Pause, RefreshCw, Volume2", "Download, Loader2, Play, Pause, RefreshCw, Volume2, Cloud, Cpu, KeyRound, Trash2, FlaskConical", 1)
s = s.replace("const previewing = ref(false)\nconst models", "const previewing = ref(false)\nconst savingGeminiKey = ref(false)\nconst testingGemini = ref(false)\nconst geminiApiKey = ref('')\nconst providers = ref<TTSProviderStatus>({ local: false, gemini: { configured: false } })\nconst models", 1)
s = s.replace("const settings = ref<TTSSettings>({ default_model: '', default_language: 'auto', number_mode: 'phone_digits', length_scale: 1 })", "const settings = ref<TTSSettings>({ default_provider: 'local', default_model: '', default_language: 'auto', number_mode: 'phone_digits', length_scale: 1, gemini_model: 'gemini-2.5-flash-preview-tts', gemini_voice: 'Kore', gemini_prompt: '' })", 1)
s = s.replace("const languages = [", "const geminiModels = [['gemini-3.1-flash-tts-preview', 'Gemini 3.1 Flash TTS Preview'], ['gemini-2.5-flash-preview-tts', 'Gemini 2.5 Flash Preview TTS'], ['gemini-2.5-pro-preview-tts', 'Gemini 2.5 Pro Preview TTS']]\nconst geminiVoices = ['Kore','Puck','Charon','Zephyr','Fenrir','Leda','Orus','Aoede','Callirrhoe','Autonoe','Enceladus','Iapetus','Umbriel','Algieba','Despina','Erinome','Algenib','Rasalgethi','Laomedeia','Achernar','Alnilam','Schedar','Gacrux','Pulcherrima','Achird','Zubenelgenubi','Vindemiatrix','Sadachbia','Sadaltager','Sulafat']\n\nconst languages = [", 1)
s = s.replace("modelDir.value = data.model_dir || ''", "modelDir.value = data.model_dir || ''\n    providers.value = data.providers || providers.value", 1)
s = s.replace("      model: settings.value.default_model,", "      provider: settings.value.default_provider,\n      model: settings.value.default_model,\n      gemini_model: settings.value.gemini_model,\n      gemini_voice: settings.value.gemini_voice,\n      gemini_prompt: settings.value.gemini_prompt,", 1)
insert_func = r'''
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
'''
s = s.replace("function stopPreview() {", insert_func + "\nfunction stopPreview() {", 1)
# Add provider selector before existing model/language grid.
needle = '''          <div class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Default Piper model</Label>'''
replacement = '''          <div class="space-y-2">
            <Label>Default TTS provider</Label>
            <Select v-model="settings.default_provider">
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="local"><span class="inline-flex items-center gap-2"><Cpu class="h-3.5 w-3.5" />Local Piper</span></SelectItem>
                <SelectItem value="gemini" :disabled="!providers.gemini.configured"><span class="inline-flex items-center gap-2"><Cloud class="h-3.5 w-3.5" />Gemini TTS{{ providers.gemini.configured ? '' : ' · add key below' }}</span></SelectItem>
              </SelectContent>
            </Select>
            <p class="text-xs text-muted-foreground">This is the default for all IVR TTS nodes. Every node can override it independently.</p>
          </div>

          <div v-if="settings.default_provider === 'local'" class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Default Piper model</Label>'''
if needle not in s: raise SystemExit('settings provider grid pattern missing')
s = s.replace(needle, replacement, 1)
# Add Gemini provider card before Installed Piper Models card.
needle = '''      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3">
          <h3 class="text-lg font-semibold text-white light:text-gray-900">Installed Piper Models</h3>'''
gemini_card = '''      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3 flex items-start justify-between gap-3">
          <div>
            <div class="flex items-center gap-2"><Cloud class="h-5 w-5 text-primary" /><h3 class="text-lg font-semibold text-white light:text-gray-900">External TTS · Gemini</h3></div>
            <p class="text-sm text-white/40 light:text-gray-500 mt-1">Gemini API TTS with automatic multilingual speech, neural voices and natural-language style control.</p>
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
          <h3 class="text-lg font-semibold text-white light:text-gray-900">Installed Piper Models</h3>'''
if needle not in s: raise SystemExit('Installed Piper card pattern missing')
s = s.replace(needle, gemini_card, 1)
p.write_text(s)

# ---------------------------------------------------------------------------
# IVR node UI: provider override + provider-specific controls.
# ---------------------------------------------------------------------------
p = Path('frontend/src/components/calling/IVRNodeProperties.vue')
s = p.read_text()
s = s.replace("const ttsDefaults = ref<TTSSettings | null>(null)", "const ttsDefaults = ref<TTSSettings | null>(null)\nconst effectiveTTSProvider = computed(() => String(config.value.tts_provider || ttsDefaults.value?.default_provider || 'local'))\nconst geminiModels = [['gemini-3.1-flash-tts-preview', 'Gemini 3.1 Flash TTS Preview'], ['gemini-2.5-flash-preview-tts', 'Gemini 2.5 Flash Preview TTS'], ['gemini-2.5-pro-preview-tts', 'Gemini 2.5 Pro Preview TTS']]\nconst geminiVoices = ['Kore','Puck','Charon','Zephyr','Fenrir','Leda','Orus','Aoede','Callirrhoe','Autonoe','Enceladus','Iapetus','Umbriel','Algieba','Despina','Erinome','Algenib','Rasalgethi','Laomedeia','Achernar','Alnilam','Schedar','Gacrux','Pulcherrima','Achird','Zubenelgenubi','Vindemiatrix','Sadachbia','Sadaltager','Sulafat']", 1)
needle = '''          <div class="rounded-lg border p-2.5 space-y-2 bg-muted/20">
            <div class="text-[11px] font-medium">TTS Options <span class="text-muted-foreground font-normal">(optional overrides)</span></div>'''
replacement = '''          <div class="rounded-lg border p-2.5 space-y-2 bg-muted/20">
            <div class="text-[11px] font-medium">TTS Options <span class="text-muted-foreground font-normal">(optional overrides)</span></div>
            <div class="space-y-1">
              <Label class="text-[10px]">Provider</Label>
              <Select :model-value="config.tts_provider || '__global__'" @update:model-value="(v: any) => updateConfig('tts_provider', v === '__global__' ? '' : v)">
                <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="__global__">Global default · {{ ttsDefaults?.default_provider || 'local' }}</SelectItem><SelectItem value="local">Local Piper</SelectItem><SelectItem value="gemini">Gemini TTS</SelectItem></SelectContent>
              </Select>
            </div>'''
if needle not in s: raise SystemExit('IVR TTS options start missing')
s = s.replace(needle, replacement, 1)
# Hide local model selector when using Gemini.
s = s.replace('<div class="space-y-1">\n                <Label class="text-[10px]">Voice model</Label>', '<div v-if="effectiveTTSProvider === \'local\'" class="space-y-1">\n                <Label class="text-[10px]">Voice model</Label>', 1)
# Hide local language and length controls.
s = s.replace('<div class="space-y-1">\n                <Label class="text-[10px]">Language / locale</Label>', '<div v-if="effectiveTTSProvider === \'local\'" class="space-y-1">\n                <Label class="text-[10px]">Language / locale</Label>', 1)
s = s.replace('<div class="space-y-1">\n                <Label class="text-[10px]">Length scale</Label>', '<div v-if="effectiveTTSProvider === \'local\'" class="space-y-1">\n                <Label class="text-[10px]">Length scale</Label>', 1)
# Insert Gemini controls before explanatory paragraph.
needle = '''            <p class="text-[10px] text-muted-foreground">Phone digit mode turns 94741682210 into 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0 before Piper speaks it.</p>'''
replacement = '''            <div v-if="effectiveTTSProvider === 'gemini'" class="space-y-2 border-t pt-2">
              <div class="grid grid-cols-2 gap-2">
                <div class="space-y-1"><Label class="text-[10px]">Gemini model</Label><Select :model-value="config.tts_gemini_model || '__global__'" @update:model-value="(v: any) => updateConfig('tts_gemini_model', v === '__global__' ? '' : v)"><SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.gemini_model || 'Gemini Flash TTS' }}</SelectItem><SelectItem v-for="item in geminiModels" :key="item[0]" :value="item[0]">{{ item[1] }}</SelectItem></SelectContent></Select></div>
                <div class="space-y-1"><Label class="text-[10px]">Gemini voice</Label><Select :model-value="config.tts_gemini_voice || '__global__'" @update:model-value="(v: any) => updateConfig('tts_gemini_voice', v === '__global__' ? '' : v)"><SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.gemini_voice || 'Kore' }}</SelectItem><SelectItem v-for="voice in geminiVoices" :key="voice" :value="voice">{{ voice }}</SelectItem></SelectContent></Select></div>
              </div>
              <div class="space-y-1"><Label class="text-[10px]">Voice direction</Label><Textarea :model-value="config.tts_gemini_prompt || ''" @update:model-value="(v: string) => updateConfig('tts_gemini_prompt', v)" placeholder="Warm, professional, brisk pace, natural Sri Lankan accent…" class="min-h-[58px] text-xs" /></div>
              <p class="text-[10px] text-muted-foreground">Gemini automatically handles supported languages from the text. The voice direction controls style, accent, pace and tone.</p>
            </div>
            <p class="text-[10px] text-muted-foreground">Phone digit mode turns 94741682210 into 9, 4, 7, 4, 1, 6, 8, 2, 2, 1, 0 before the selected provider speaks it.</p>'''
if needle not in s: raise SystemExit('IVR number mode explanation missing')
s = s.replace(needle, replacement, 1)
p.write_text(s)

print('TTS provider implementation applied')
