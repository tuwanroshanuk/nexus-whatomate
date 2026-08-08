from pathlib import Path


def must_replace(path, old, new, count=1):
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f'pattern not found in {path}: {old[:160]!r}')
    p.write_text(s.replace(old, new, count))

# This runs after apply_tts_providers.py and extends the generated provider layer.

# -------------------- backend settings/provider engine --------------------
p = Path('internal/tts/piper.go')
s = p.read_text()
s = s.replace('\tGeminiPrompt    string  `json:"gemini_prompt"`\n}', '\tGeminiPrompt    string  `json:"gemini_prompt"`\n\tGoogleCloudVoice string `json:"google_cloud_voice"`\n\tGoogleCloudLanguage string `json:"google_cloud_language"`\n}', 1)
s = s.replace('\t\tGeminiVoice:     "Kore",\n\t}', '\t\tGeminiVoice:     "Kore",\n\t\tGoogleCloudLanguage: "en-US",\n\t}', 1)
s = s.replace('settings.DefaultProvider != "local" && settings.DefaultProvider != "gemini"', 'settings.DefaultProvider != "local" && settings.DefaultProvider != "gemini" && settings.DefaultProvider != "google_cloud"')
s = s.replace('if settings.DefaultProvider == "gemini" {\n\t\tif configured, _ := p.GeminiConfigured(); !configured {\n\t\t\treturn Settings{}, fmt.Errorf("Gemini TTS credentials are not configured")\n\t\t}\n\t}', 'if settings.DefaultProvider == "gemini" {\n\t\tif configured, _ := p.GeminiConfigured(); !configured {\n\t\t\treturn Settings{}, fmt.Errorf("Gemini TTS credentials are not configured")\n\t\t}\n\t}\n\tif settings.DefaultProvider == "google_cloud" {\n\t\tif configured, _ := p.GoogleCloudConfigured(); !configured {\n\t\t\treturn Settings{}, fmt.Errorf("Google Cloud TTS credentials are not configured")\n\t\t}\n\t\tif strings.TrimSpace(settings.GoogleCloudVoice) == "" {\n\t\t\treturn Settings{}, fmt.Errorf("select a Google Cloud TTS voice")\n\t\t}\n\t}', 1)
s = s.replace('\tif provider == "gemini" {\n\t\treturn p.generateGemini(PrepareText(text, mode), config, settings)\n\t}\n\tif provider != "local" {', '\tif provider == "gemini" {\n\t\treturn p.generateGemini(PrepareText(text, mode), config, settings)\n\t}\n\tif provider == "google_cloud" {\n\t\treturn p.generateGoogleCloud(PrepareText(text, mode), config, settings)\n\t}\n\tif provider != "local" {', 1)
p.write_text(s)

p = Path('internal/tts/providers.go')
s = p.read_text()
s = s.replace('"golang.org/x/oauth2"', '"golang.org/x/oauth2"') if '"golang.org/x/oauth2"' in s else s
# add imports
s = s.replace('\t"time"\n)', '\t"time"\n\n\t"golang.org/x/oauth2"\n\t"golang.org/x/oauth2/google"\n)', 1)
s = s.replace('type ProviderStatus struct {\n\tLocal  bool                 `json:"local"`\n\tGemini GeminiProviderStatus `json:"gemini"`\n}', 'type ProviderStatus struct {\n\tLocal       bool                      `json:"local"`\n\tGemini      GeminiProviderStatus      `json:"gemini"`\n\tGoogleCloud GoogleCloudProviderStatus `json:"google_cloud"`\n}\n\ntype GoogleCloudProviderStatus struct {\n\tConfigured bool `json:"configured"`\n\tProjectID string `json:"project_id,omitempty"`\n}', 1)
s = s.replace('type encryptedSecrets struct {\n\tVersion      int    `json:"version"`\n\tGeminiAPIKey string `json:"gemini_api_key,omitempty"`\n}', 'type encryptedSecrets struct {\n\tVersion                   int    `json:"version"`\n\tGeminiAPIKey              string `json:"gemini_api_key,omitempty"`\n\tGoogleCloudServiceAccount string `json:"google_cloud_service_account,omitempty"`\n}', 1)
s = s.replace('return ProviderStatus{Local: local, Gemini: GeminiProviderStatus{Configured: configured}}', 'cloudConfigured, _ := p.GoogleCloudConfigured()\n\tprojectID := ""\n\tif cloudConfigured {\n\t\tif raw, err := p.googleCloudCredentialsJSON(); err == nil {\n\t\t\tvar metadata struct { ProjectID string `json:"project_id"` }\n\t\t\t_ = json.Unmarshal([]byte(raw), &metadata)\n\t\t\tprojectID = metadata.ProjectID\n\t\t}\n\t}\n\treturn ProviderStatus{Local: local, Gemini: GeminiProviderStatus{Configured: configured}, GoogleCloud: GoogleCloudProviderStatus{Configured: cloudConfigured, ProjectID: projectID}}', 1)
append = r'''

type GoogleCloudVoice struct {
    Name                    string   `json:"name"`
    LanguageCodes           []string `json:"language_codes"`
    Gender                  string   `json:"gender"`
    NaturalSampleRateHertz  int      `json:"natural_sample_rate_hertz"`
    Tier                    string   `json:"tier"`
    FreeLimitCharacters     int      `json:"free_limit_characters"`
}

type googleCloudVoicesResponse struct {
    Voices []struct {
        LanguageCodes          []string `json:"languageCodes"`
        Name                   string   `json:"name"`
        SsmlGender             string   `json:"ssmlGender"`
        NaturalSampleRateHertz int      `json:"naturalSampleRateHertz"`
    } `json:"voices"`
}

type googleCloudSynthesizeResponse struct {
    AudioContent string `json:"audioContent"`
}

func (p *PiperTTS) SetGoogleCloudCredentials(serviceAccountJSON string) error {
    serviceAccountJSON = strings.TrimSpace(serviceAccountJSON)
    if serviceAccountJSON == "" { return fmt.Errorf("service account JSON is required") }
    var parsed struct {
        Type        string `json:"type"`
        ProjectID   string `json:"project_id"`
        ClientEmail string `json:"client_email"`
        PrivateKey  string `json:"private_key"`
    }
    if err := json.Unmarshal([]byte(serviceAccountJSON), &parsed); err != nil {
        return fmt.Errorf("service account JSON is invalid: %w", err)
    }
    if parsed.Type != "service_account" || parsed.ProjectID == "" || parsed.ClientEmail == "" || parsed.PrivateKey == "" {
        return fmt.Errorf("a Google Cloud service-account JSON credential is required")
    }
    if _, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), "https://www.googleapis.com/auth/cloud-platform"); err != nil {
        return fmt.Errorf("could not parse Google Cloud service-account credential: %w", err)
    }
    encrypted, err := p.encryptSecret(serviceAccountJSON)
    if err != nil { return err }
    p.mu.Lock()
    defer p.mu.Unlock()
    secrets, err := p.readSecrets()
    if err != nil { return err }
    secrets.GoogleCloudServiceAccount = encrypted
    return p.writeSecrets(secrets)
}

func (p *PiperTTS) ClearGoogleCloudCredentials() error {
    p.mu.Lock()
    defer p.mu.Unlock()
    secrets, err := p.readSecrets()
    if err != nil { return err }
    secrets.GoogleCloudServiceAccount = ""
    return p.writeSecrets(secrets)
}

func (p *PiperTTS) GoogleCloudConfigured() (bool, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    secrets, err := p.readSecrets()
    if err != nil { return false, err }
    return secrets.GoogleCloudServiceAccount != "", nil
}

func (p *PiperTTS) googleCloudCredentialsJSON() (string, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    secrets, err := p.readSecrets()
    if err != nil { return "", err }
    if secrets.GoogleCloudServiceAccount == "" { return "", fmt.Errorf("Google Cloud TTS credentials are not configured") }
    return p.decryptSecret(secrets.GoogleCloudServiceAccount)
}

func googleCloudVoiceTier(name string) (string, int) {
    lower := strings.ToLower(name)
    switch {
    case strings.Contains(lower, "chirp3-hd"):
        return "chirp3_hd", 1_000_000
    case strings.Contains(lower, "neural2"):
        return "neural2", 1_000_000
    case strings.Contains(lower, "wavenet"):
        return "wavenet", 4_000_000
    case strings.Contains(lower, "standard"):
        return "standard", 4_000_000
    case strings.Contains(lower, "studio"):
        return "studio", 1_000_000
    default:
        return "other", 0
    }
}

func (p *PiperTTS) googleCloudToken(ctx context.Context) (*oauth2.Token, error) {
    raw, err := p.googleCloudCredentialsJSON()
    if err != nil { return nil, err }
    cfg, err := google.JWTConfigFromJSON([]byte(raw), "https://www.googleapis.com/auth/cloud-platform")
    if err != nil { return nil, err }
    if p.HTTPClient != nil {
        ctx = context.WithValue(ctx, oauth2.HTTPClient, p.HTTPClient)
    }
    return cfg.TokenSource(ctx).Token()
}

func (p *PiperTTS) ListGoogleCloudVoices(ctx context.Context) ([]GoogleCloudVoice, error) {
    token, err := p.googleCloudToken(ctx)
    if err != nil { return nil, err }
    client := p.HTTPClient
    if client == nil { client = &http.Client{Timeout: 30 * time.Second} }
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://texttospeech.googleapis.com/v1/voices", nil)
    if err != nil { return nil, err }
    req.Header.Set("Authorization", "Bearer "+token.AccessToken)
    resp, err := client.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
    if err != nil { return nil, err }
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return nil, fmt.Errorf("Google Cloud TTS voices request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
    }
    var parsed googleCloudVoicesResponse
    if err := json.Unmarshal(data, &parsed); err != nil { return nil, err }
    voices := make([]GoogleCloudVoice, 0, len(parsed.Voices))
    for _, voice := range parsed.Voices {
        tier, free := googleCloudVoiceTier(voice.Name)
        voices = append(voices, GoogleCloudVoice{
            Name: voice.Name, LanguageCodes: voice.LanguageCodes, Gender: voice.SsmlGender,
            NaturalSampleRateHertz: voice.NaturalSampleRateHertz, Tier: tier, FreeLimitCharacters: free,
        })
    }
    sort.Slice(voices, func(i, j int) bool {
        if voices[i].Tier == voices[j].Tier { return voices[i].Name < voices[j].Name }
        return voices[i].Tier < voices[j].Tier
    })
    return voices, nil
}

func (p *PiperTTS) generateGoogleCloud(text string, config map[string]any, settings Settings) (string, error) {
    voice := configString(config, "tts_google_cloud_voice")
    if voice == "" { voice = settings.GoogleCloudVoice }
    if voice == "" { return "", fmt.Errorf("Google Cloud TTS voice is not selected") }
    language := configString(config, "tts_google_cloud_language")
    if language == "" { language = settings.GoogleCloudLanguage }
    if language == "" {
        parts := strings.Split(voice, "-")
        if len(parts) >= 2 { language = parts[0] + "-" + parts[1] }
    }
    rate := configFloat(config, "tts_google_cloud_speaking_rate")
    if rate == 0 { rate = 1.0 }
    if rate < 0.25 || rate > 4 { rate = 1.0 }
    tier, _ := googleCloudVoiceTier(voice)

    cacheKey := "google-cloud\x00" + voice + "\x00" + language + "\x00" + fmt.Sprintf("%.3f", rate) + "\x00" + text
    filename := "tts_" + sha256Short(cacheKey) + ".ogg"
    outPath := filepath.Join(p.AudioDir, filename)
    if fileExists(outPath) { return filename, nil }
    if err := os.MkdirAll(p.AudioDir, 0755); err != nil { return "", err }

    audioConfig := map[string]any{"audioEncoding": "LINEAR16"}
    if tier != "chirp3_hd" { audioConfig["speakingRate"] = rate }
    body := map[string]any{
        "input": map[string]any{"text": text},
        "voice": map[string]any{"languageCode": language, "name": voice},
        "audioConfig": audioConfig,
    }
    payload, _ := json.Marshal(body)
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()
    token, err := p.googleCloudToken(ctx)
    if err != nil { return "", err }
    client := p.HTTPClient
    if client == nil { client = &http.Client{Timeout: 60 * time.Second} }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://texttospeech.googleapis.com/v1/text:synthesize", bytes.NewReader(payload))
    if err != nil { return "", err }
    req.Header.Set("Authorization", "Bearer "+token.AccessToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := client.Do(req)
    if err != nil { return "", err }
    defer resp.Body.Close()
    data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
    if err != nil { return "", err }
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return "", fmt.Errorf("Google Cloud TTS returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
    }
    var parsed googleCloudSynthesizeResponse
    if err := json.Unmarshal(data, &parsed); err != nil { return "", err }
    if parsed.AudioContent == "" { return "", fmt.Errorf("Google Cloud TTS response did not contain audio") }
    wav, err := base64.StdEncoding.DecodeString(parsed.AudioContent)
    if err != nil { return "", err }
    wavPath := outPath + ".tmp.wav"
    tmpOgg := outPath + ".tmp.ogg"
    defer func() { _ = os.Remove(wavPath); _ = os.Remove(tmpOgg) }()
    if err := os.WriteFile(wavPath, wav, 0600); err != nil { return "", err }
    cmd := exec.Command("ffmpeg", "-y", "-i", wavPath, "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k", "-application", "audio", "-vn", tmpOgg)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil { return "", fmt.Errorf("Google Cloud TTS audio conversion failed: %w (stderr: %s)", err, stderr.String()) }
    if err := os.Rename(tmpOgg, outPath); err != nil { return "", err }
    return filename, nil
}
'''
# providers.go needs sort import for cloud voice sorting.
s = s.replace('\t"path/filepath"\n', '\t"path/filepath"\n\t"sort"\n', 1)
s += append
p.write_text(s)

# -------------------- handlers/routes --------------------
p = Path('internal/handlers/tts_providers.go')
s = p.read_text()
s += r'''

type googleCloudCredentialRequest struct {
    ServiceAccountJSON string `json:"service_account_json"`
}

func (a *App) UpdateGoogleCloudTTSCredentials(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    var req googleCloudCredentialRequest
    if err := a.decodeRequest(r, &req); err != nil { return nil }
    if err := a.TTS.SetGoogleCloudCredentials(req.ServiceAccountJSON); err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
    }
    a.Log.Info("Google Cloud TTS credentials updated")
    status := a.TTS.GetProviderStatus()
    return r.SendEnvelope(status.GoogleCloud)
}

func (a *App) DeleteGoogleCloudTTSCredentials(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    if a.TTS.GetSettings().DefaultProvider == "google_cloud" {
        return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Switch the default TTS provider away from Google Cloud before removing its credentials", nil, "")
    }
    if err := a.TTS.ClearGoogleCloudCredentials(); err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not remove Google Cloud TTS credentials", nil, "")
    }
    return r.SendEnvelope(map[string]any{"configured": false})
}

func (a *App) ListGoogleCloudTTSVoices(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    ctx, cancel := context.WithTimeout(r.RequestCtx, 30*time.Second)
    defer cancel()
    voices, err := a.TTS.ListGoogleCloudVoices(ctx)
    if err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not load Google Cloud TTS voices: "+err.Error(), nil, "")
    }
    return r.SendEnvelope(map[string]any{"voices": voices})
}

func (a *App) TestGoogleCloudTTSProvider(r *fastglue.Request) error {
    if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil { return nil }
    if !a.requireTTS(r) { return nil }
    settings := a.TTS.GetSettings()
    if strings.TrimSpace(settings.GoogleCloudVoice) == "" {
        return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Select and save a Google Cloud voice first", nil, "")
    }
    filename, err := a.TTS.GenerateWithConfig("Hello. Google Cloud text to speech is connected successfully.", map[string]any{
        "tts_provider": "google_cloud",
        "tts_google_cloud_voice": settings.GoogleCloudVoice,
        "tts_google_cloud_language": settings.GoogleCloudLanguage,
    })
    if err != nil {
        return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Google Cloud TTS test failed: "+err.Error(), nil, "")
    }
    return r.SendEnvelope(map[string]any{"ok": true, "filename": filename})
}
'''
s = s.replace('import (\n\t"strings"', 'import (\n\t"context"\n\t"strings"\n\t"time"', 1)
p.write_text(s)

p = Path('cmd/whatomate/main.go')
s = p.read_text()
needle = 'g.POST("/api/tts/providers/gemini/test", app.TestGeminiTTSProvider)\n\tg.POST("/api/tts/preview", app.PreviewTTS)'
repl = 'g.POST("/api/tts/providers/gemini/test", app.TestGeminiTTSProvider)\n\tg.PUT("/api/tts/providers/google-cloud", app.UpdateGoogleCloudTTSCredentials)\n\tg.DELETE("/api/tts/providers/google-cloud", app.DeleteGoogleCloudTTSCredentials)\n\tg.GET("/api/tts/providers/google-cloud/voices", app.ListGoogleCloudTTSVoices)\n\tg.POST("/api/tts/providers/google-cloud/test", app.TestGoogleCloudTTSProvider)\n\tg.POST("/api/tts/preview", app.PreviewTTS)'
if needle not in s: raise SystemExit('provider route insertion point missing')
s = s.replace(needle, repl, 1)
p.write_text(s)

# Preview request/config supports cloud provider too.
p = Path('internal/handlers/tts_settings.go')
s = p.read_text()
s = s.replace('\tGeminiPrompt string  `json:"gemini_prompt"`\n', '\tGeminiPrompt string  `json:"gemini_prompt"`\n\tGoogleCloudVoice string `json:"google_cloud_voice"`\n\tGoogleCloudLanguage string `json:"google_cloud_language"`\n\tGoogleCloudSpeakingRate float64 `json:"google_cloud_speaking_rate"`\n', 1)
s = s.replace('\t\t"tts_gemini_prompt": req.GeminiPrompt,\n', '\t\t"tts_gemini_prompt": req.GeminiPrompt,\n\t\t"tts_google_cloud_voice": req.GoogleCloudVoice,\n\t\t"tts_google_cloud_language": req.GoogleCloudLanguage,\n\t\t"tts_google_cloud_speaking_rate": req.GoogleCloudSpeakingRate,\n', 1)
p.write_text(s)

# -------------------- frontend API --------------------
p = Path('frontend/src/services/api.ts')
s = p.read_text()
s = s.replace("default_provider: 'local' | 'gemini'", "default_provider: 'local' | 'gemini' | 'google_cloud'", 1)
s = s.replace('  gemini_prompt: string\n}', '  gemini_prompt: string\n  google_cloud_voice: string\n  google_cloud_language: string\n}', 1)
s = s.replace("export interface TTSProviderStatus {\n  local: boolean\n  gemini: { configured: boolean }\n}", "export interface TTSProviderStatus {\n  local: boolean\n  gemini: { configured: boolean }\n  google_cloud: { configured: boolean; project_id?: string }\n}\n\nexport interface GoogleCloudTTSVoice {\n  name: string\n  language_codes: string[]\n  gender: string\n  natural_sample_rate_hertz: number\n  tier: 'standard' | 'wavenet' | 'neural2' | 'studio' | 'chirp3_hd' | 'other'\n  free_limit_characters: number\n}", 1)
s = s.replace("  setGeminiCredentials: (apiKey: string) =>", "  setGoogleCloudCredentials: (serviceAccountJson: string) => api.put('/tts/providers/google-cloud', { service_account_json: serviceAccountJson }),\n  clearGoogleCloudCredentials: () => api.delete('/tts/providers/google-cloud'),\n  getGoogleCloudVoices: () => api.get<{ voices: GoogleCloudTTSVoice[] }>('/tts/providers/google-cloud/voices', { timeout: 30 * 1000 }),\n  testGoogleCloud: () => api.post<{ ok: boolean; filename: string }>('/tts/providers/google-cloud/test', {}, { timeout: 90 * 1000 }),\n  setGeminiCredentials: (apiKey: string) =>", 1)
s = s.replace("gemini_prompt?: string }) =>", "gemini_prompt?: string; google_cloud_voice?: string; google_cloud_language?: string; google_cloud_speaking_rate?: number }) =>", 1)
p.write_text(s)

# -------------------- settings UI fixes + cloud UI --------------------
p = Path('frontend/src/components/settings/TTSSettingsPanel.vue')
s = p.read_text()
# Fix the declarations missed by first script because uninstall state sits between previewing and models.
s = s.replace("const previewing = ref(false)\nconst uninstalling", "const previewing = ref(false)\nconst savingGeminiKey = ref(false)\nconst testingGemini = ref(false)\nconst geminiApiKey = ref('')\nconst providers = ref<TTSProviderStatus>({ local: false, gemini: { configured: false }, google_cloud: { configured: false } })\nconst savingCloudCredentials = ref(false)\nconst testingCloud = ref(false)\nconst loadingCloudVoices = ref(false)\nconst googleCloudServiceAccount = ref('')\nconst googleCloudVoices = ref<GoogleCloudTTSVoice[]>([])\nconst uninstalling", 1)
s = s.replace("type TTSModelInfo, type TTSSettings, type TTSProviderStatus", "type TTSModelInfo, type TTSSettings, type TTSProviderStatus, type GoogleCloudTTSVoice", 1)
s = s.replace("const settings = ref<TTSSettings>({ default_provider: 'local', default_model: '', default_language: 'auto', number_mode: 'phone_digits', length_scale: 1, gemini_model: 'gemini-2.5-flash-preview-tts', gemini_voice: 'Kore', gemini_prompt: '' })", "const settings = ref<TTSSettings>({ default_provider: 'local', default_model: '', default_language: 'auto', number_mode: 'phone_digits', length_scale: 1, gemini_model: 'gemini-2.5-flash-preview-tts', gemini_voice: 'Kore', gemini_prompt: '', google_cloud_voice: '', google_cloud_language: 'en-US' })", 1)
# Import GoogleCloudTTSVoice type may not be there if previous exact differs
if 'type GoogleCloudTTSVoice' not in s.split('\n')[2]:
    s = s.replace("type TTSProviderStatus }", "type TTSProviderStatus, type GoogleCloudTTSVoice }", 1)
# After load provider status, fetch voices if configured.
s = s.replace("providers.value = data.providers || providers.value", "providers.value = data.providers || providers.value\n    if (providers.value.google_cloud?.configured && !googleCloudVoices.value.length) void loadGoogleCloudVoices()", 1)
# Add helper computed tier labels before speedLabel.
s = s.replace("const speedLabel = computed", "const cloudTierLabel = (tier: string) => ({ standard: 'Standard · 4M free/mo', wavenet: 'WaveNet · 4M free/mo', neural2: 'Neural2 · 1M free/mo', chirp3_hd: 'Chirp 3 HD · 1M free/mo', studio: 'Studio · 1M free/mo', other: 'Other' } as Record<string,string>)[tier] || tier\nconst selectedCloudVoice = computed(() => googleCloudVoices.value.find(v => v.name === settings.value.google_cloud_voice))\n\nconst speedLabel = computed", 1)
# Add service methods before saveGeminiCredentials.
marker = 'async function saveGeminiCredentials() {'
cloud_funcs = r'''async function loadGoogleCloudVoices() {
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

'''
if marker not in s: raise SystemExit('gemini function marker missing')
s = s.replace(marker, cloud_funcs + marker, 1)
# Preview cloud fields.
s = s.replace("gemini_prompt: settings.value.gemini_prompt,", "gemini_prompt: settings.value.gemini_prompt,\n      google_cloud_voice: settings.value.google_cloud_voice,\n      google_cloud_language: settings.value.google_cloud_language,", 1)
# Add provider option.
s = s.replace("<SelectItem value=\"gemini\" :disabled=\"!providers.gemini.configured\"><span", "<SelectItem value=\"google_cloud\" :disabled=\"!providers.google_cloud.configured\"><span class=\"inline-flex items-center gap-2\"><Cloud class=\"h-3.5 w-3.5\" />Google Cloud TTS{{ providers.google_cloud.configured ? '' : ' · add credentials below' }}</span></SelectItem>\n                <SelectItem value=\"gemini\" :disabled=\"!providers.gemini.configured\"><span", 1)
# Add Cloud selection card in defaults, before number pronunciation grid.
needle = '''          <div class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Number pronunciation</Label>'''
cloud_defaults = '''          <div v-if="settings.default_provider === 'google_cloud'" class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Google Cloud voice</Label>
              <Select v-model="settings.google_cloud_voice">
                <SelectTrigger><SelectValue placeholder="Select a Cloud TTS voice" /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="voice in googleCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }} · {{ cloudTierLabel(voice.tier) }}</SelectItem>
                </SelectContent>
              </Select>
              <Button variant="outline" size="sm" :disabled="loadingCloudVoices" @click="loadGoogleCloudVoices"><Loader2 v-if="loadingCloudVoices" class="h-4 w-4 animate-spin" /><RefreshCw v-else class="h-4 w-4" />Refresh voices</Button>
            </div>
            <div class="space-y-2">
              <Label>Language code</Label>
              <Input v-model="settings.google_cloud_language" placeholder="en-US" />
              <p v-if="selectedCloudVoice" class="text-xs text-muted-foreground">{{ cloudTierLabel(selectedCloudVoice.tier) }} · {{ selectedCloudVoice.gender }} · {{ selectedCloudVoice.language_codes.join(', ') }}</p>
            </div>
          </div>

          <div class="grid md:grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>Number pronunciation</Label>'''
if needle not in s: raise SystemExit('number grid marker missing')
s = s.replace(needle, cloud_defaults, 1)
# Add cloud credentials card before Gemini card.
needle = '''      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
        <div class="p-6 pb-3 flex items-start justify-between gap-3">
          <div>
            <div class="flex items-center gap-2"><Cloud class="h-5 w-5 text-primary" /><h3 class="text-lg font-semibold text-white light:text-gray-900">External TTS · Gemini</h3>'''
cloud_card = '''      <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
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
            <div class="flex items-center gap-2"><Cloud class="h-5 w-5 text-primary" /><h3 class="text-lg font-semibold text-white light:text-gray-900">External TTS · Gemini</h3>'''
if needle not in s: raise SystemExit('Gemini card marker missing')
s = s.replace(needle, cloud_card, 1)
# Clarify Gemini paid.
s = s.replace('Gemini API TTS with automatic multilingual speech, neural voices and natural-language style control.', 'Gemini API TTS with automatic multilingual speech, neural voices and natural-language style control. Gemini TTS has no free usage tier.', 1)
p.write_text(s)

# -------------------- IVR node: cloud provider selector/options --------------------
p = Path('frontend/src/components/calling/IVRNodeProperties.vue')
s = p.read_text()
# Expand imports/status and state
s = s.replace("type TTSModelInfo, type TTSSettings", "type TTSModelInfo, type TTSSettings, type GoogleCloudTTSVoice", 1)
s = s.replace("const ttsDefaults = ref<TTSSettings | null>(null)", "const ttsDefaults = ref<TTSSettings | null>(null)\nconst googleCloudVoices = ref<GoogleCloudTTSVoice[]>([])", 1)
s = s.replace("ttsDefaults.value = data.settings || null", "ttsDefaults.value = data.settings || null\n    if (data.providers?.google_cloud?.configured) {\n      try {\n        const voicesRes = await ttsService.getGoogleCloudVoices()\n        const voicesData = (voicesRes.data as any)?.data || voicesRes.data\n        googleCloudVoices.value = voicesData.voices || []\n      } catch {}\n    }", 1)
# Provider option
s = s.replace('<SelectItem value="local">Local Piper</SelectItem><SelectItem value="gemini">Gemini TTS</SelectItem>', '<SelectItem value="local">Local Piper</SelectItem><SelectItem value="google_cloud">Google Cloud TTS</SelectItem><SelectItem value="gemini">Gemini TTS</SelectItem>', 1)
# Insert cloud controls before Gemini controls.
needle = '''            <div v-if="effectiveTTSProvider === 'gemini'" class="space-y-2 border-t pt-2">'''
cloud_node = '''            <div v-if="effectiveTTSProvider === 'google_cloud'" class="space-y-2 border-t pt-2">
              <div class="space-y-1">
                <Label class="text-[10px]">Google Cloud voice</Label>
                <Select :model-value="config.tts_google_cloud_voice || '__global__'" @update:model-value="(v: any) => updateConfig('tts_google_cloud_voice', v === '__global__' ? '' : v)">
                  <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                  <SelectContent><SelectItem value="__global__">Global · {{ ttsDefaults?.google_cloud_voice || 'not selected' }}</SelectItem><SelectItem v-for="voice in googleCloudVoices" :key="voice.name" :value="voice.name">{{ voice.name }}</SelectItem></SelectContent>
                </Select>
              </div>
              <div class="grid grid-cols-2 gap-2">
                <div class="space-y-1"><Label class="text-[10px]">Language</Label><Input :model-value="config.tts_google_cloud_language || ''" @update:model-value="(v: string) => updateConfig('tts_google_cloud_language', v)" :placeholder="ttsDefaults?.google_cloud_language || 'en-US'" class="h-7 text-xs" /></div>
                <div class="space-y-1"><Label class="text-[10px]">Speaking rate</Label><Input type="number" min="0.25" max="4" step="0.05" :model-value="config.tts_google_cloud_speaking_rate || 1" @update:model-value="(v: string | number) => updateConfig('tts_google_cloud_speaking_rate', Number(v) || 1)" class="h-7 text-xs" /></div>
              </div>
              <p class="text-[10px] text-muted-foreground">Standard/WaveNet include up to 4M free characters monthly; Neural2 and Chirp 3 HD include up to 1M. Chirp 3 HD ignores speaking-rate controls.</p>
            </div>
            <div v-if="effectiveTTSProvider === 'gemini'" class="space-y-2 border-t pt-2">'''
if needle not in s: raise SystemExit('Gemini node options marker missing')
s = s.replace(needle, cloud_node, 1)
p.write_text(s)

print('Google Cloud TTS voice tiers extension applied')
