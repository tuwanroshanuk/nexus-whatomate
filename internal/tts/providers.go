package tts

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
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type ProviderStatus struct {
	Local       bool                      `json:"local"`
	Gemini      GeminiProviderStatus      `json:"gemini"`
	GoogleCloud GoogleCloudProviderStatus `json:"google_cloud"`
}

type GoogleCloudProviderStatus struct {
	Configured bool   `json:"configured"`
	ProjectID  string `json:"project_id,omitempty"`
}

type GeminiProviderStatus struct {
	Configured bool `json:"configured"`
}

type encryptedSecrets struct {
	Version                   int    `json:"version"`
	GeminiAPIKey              string `json:"gemini_api_key,omitempty"`
	GoogleCloudServiceAccount string `json:"google_cloud_service_account,omitempty"`
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
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), []byte("whatomate-tts-secret-v1"))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (p *PiperTTS) decryptSecret(value string) (string, error) {
	key, err := p.credentialKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted credential is invalid")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte("whatomate-tts-secret-v1"))
	if err != nil {
		return "", fmt.Errorf("could not decrypt external TTS credential")
	}
	return string(plain), nil
}

func (p *PiperTTS) readSecrets() (encryptedSecrets, error) {
	var out encryptedSecrets
	data, err := os.ReadFile(p.secretsPath())
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (p *PiperTTS) writeSecrets(secrets encryptedSecrets) error {
	if err := os.MkdirAll(filepath.Dir(p.secretsPath()), 0755); err != nil {
		return err
	}
	secrets.Version = 1
	data, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.secretsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p.secretsPath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (p *PiperTTS) GeminiConfigured() (bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return false, err
	}
	return secrets.GeminiAPIKey != "", nil
}

func (p *PiperTTS) GetProviderStatus() ProviderStatus {
	configured, _ := p.GeminiConfigured()
	local := strings.TrimSpace(p.BinaryPath) != "" && (strings.TrimSpace(p.ModelPath) != "" || strings.TrimSpace(p.ModelDir) != "")
	cloudConfigured, _ := p.GoogleCloudConfigured()
	projectID := ""
	if cloudConfigured {
		if raw, err := p.googleCloudCredentialsJSON(); err == nil {
			var metadata struct {
				ProjectID string `json:"project_id"`
			}
			_ = json.Unmarshal([]byte(raw), &metadata)
			projectID = metadata.ProjectID
		}
	}
	return ProviderStatus{Local: local, Gemini: GeminiProviderStatus{Configured: configured}, GoogleCloud: GoogleCloudProviderStatus{Configured: cloudConfigured, ProjectID: projectID}}
}

func (p *PiperTTS) SetGeminiAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("Gemini API key is required")
	}
	encrypted, err := p.encryptSecret(apiKey)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return err
	}
	secrets.GeminiAPIKey = encrypted
	return p.writeSecrets(secrets)
}

func (p *PiperTTS) ClearGeminiAPIKey() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return err
	}
	secrets.GeminiAPIKey = ""
	return p.writeSecrets(secrets)
}

func (p *PiperTTS) geminiAPIKey() (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return "", err
	}
	if secrets.GeminiAPIKey == "" {
		return "", fmt.Errorf("Gemini TTS credentials are not configured")
	}
	return p.decryptSecret(secrets.GeminiAPIKey)
}

func geminiPrompt(text, direction string) string {
	direction = strings.TrimSpace(direction)
	if direction == "" {
		return text
	}
	return "Synthesize speech from the transcript below. Do not speak these instructions. Voice direction: " + direction + "\n\nTranscript:\n" + text
}

func (p *PiperTTS) generateGemini(text string, config map[string]any, settings Settings) (string, error) {
	key, err := p.geminiAPIKey()
	if err != nil {
		return "", err
	}
	model := configString(config, "tts_gemini_model")
	if model == "" {
		model = settings.GeminiModel
	}
	if model == "" {
		model = "gemini-2.5-flash-preview-tts"
	}
	voice := configString(config, "tts_gemini_voice")
	if voice == "" {
		voice = settings.GeminiVoice
	}
	if voice == "" {
		voice = "Kore"
	}
	prompt := configString(config, "tts_gemini_prompt")
	if prompt == "" {
		prompt = settings.GeminiPrompt
	}

	cacheKey := "gemini\x00" + model + "\x00" + voice + "\x00" + prompt + "\x00" + text
	filename := "tts_" + sha256Short(cacheKey) + ".ogg"
	outPath := filepath.Join(p.AudioDir, filename)
	if fileExists(outPath) {
		return filename, nil
	}
	if err := os.MkdirAll(p.AudioDir, 0755); err != nil {
		return "", err
	}

	body := map[string]any{
		"contents": []any{map[string]any{"parts": []any{map[string]any{"text": geminiPrompt(text, prompt)}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}},
		},
	}
	payload, _ := json.Marshal(body)
	endpoint := "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent"

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	localClient := *client
	localClient.Timeout = 90 * time.Second

	var decoded []byte
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", key)
		resp, err := localClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		responseData, readErr := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = readErr
			continue
		}
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
			if parsed.Error != nil && parsed.Error.Message != "" {
				message += ": " + parsed.Error.Message
			}
			return "", fmt.Errorf("%s", message)
		}
		if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 || parsed.Candidates[0].Content.Parts[0].InlineData.Data == "" {
			return "", fmt.Errorf("Gemini TTS response did not contain audio")
		}
		decoded, err = base64.StdEncoding.DecodeString(parsed.Candidates[0].Content.Parts[0].InlineData.Data)
		if err != nil {
			return "", fmt.Errorf("could not decode Gemini TTS audio: %w", err)
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return "", fmt.Errorf("Gemini TTS request failed: %w", lastErr)
	}

	pcmPath := outPath + ".tmp.pcm"
	tmpOgg := outPath + ".tmp.ogg"
	defer func() { _ = os.Remove(pcmPath); _ = os.Remove(tmpOgg) }()
	if err := os.WriteFile(pcmPath, decoded, 0600); err != nil {
		return "", err
	}
	cmd := exec.Command("ffmpeg", "-y", "-f", "s16le", "-ar", "24000", "-ac", "1", "-i", pcmPath,
		"-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k", "-application", "audio", "-vn", tmpOgg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Gemini audio conversion failed: %w (stderr: %s)", err, stderr.String())
	}
	if err := os.Rename(tmpOgg, outPath); err != nil {
		return "", err
	}
	return filename, nil
}

type GoogleCloudVoice struct {
	Name                   string   `json:"name"`
	LanguageCodes          []string `json:"language_codes"`
	Gender                 string   `json:"gender"`
	NaturalSampleRateHertz int      `json:"natural_sample_rate_hertz"`
	Tier                   string   `json:"tier"`
	FreeLimitCharacters    int      `json:"free_limit_characters"`
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
	if serviceAccountJSON == "" {
		return fmt.Errorf("service account JSON is required")
	}
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
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return err
	}
	secrets.GoogleCloudServiceAccount = encrypted
	return p.writeSecrets(secrets)
}

func (p *PiperTTS) ClearGoogleCloudCredentials() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return err
	}
	secrets.GoogleCloudServiceAccount = ""
	return p.writeSecrets(secrets)
}

func (p *PiperTTS) GoogleCloudConfigured() (bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return false, err
	}
	return secrets.GoogleCloudServiceAccount != "", nil
}

func (p *PiperTTS) googleCloudCredentialsJSON() (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	secrets, err := p.readSecrets()
	if err != nil {
		return "", err
	}
	if secrets.GoogleCloudServiceAccount == "" {
		return "", fmt.Errorf("Google Cloud TTS credentials are not configured")
	}
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
	if err != nil {
		return nil, err
	}
	cfg, err := google.JWTConfigFromJSON([]byte(raw), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, err
	}
	if p.HTTPClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, p.HTTPClient)
	}
	return cfg.TokenSource(ctx).Token()
}

func (p *PiperTTS) ListGoogleCloudVoices(ctx context.Context) ([]GoogleCloudVoice, error) {
	token, err := p.googleCloudToken(ctx)
	if err != nil {
		return nil, err
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://texttospeech.googleapis.com/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Google Cloud TTS voices request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed googleCloudVoicesResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	voices := make([]GoogleCloudVoice, 0, len(parsed.Voices))
	for _, voice := range parsed.Voices {
		tier, free := googleCloudVoiceTier(voice.Name)
		voices = append(voices, GoogleCloudVoice{
			Name: voice.Name, LanguageCodes: voice.LanguageCodes, Gender: voice.SsmlGender,
			NaturalSampleRateHertz: voice.NaturalSampleRateHertz, Tier: tier, FreeLimitCharacters: free,
		})
	}
	sort.Slice(voices, func(i, j int) bool {
		if voices[i].Tier == voices[j].Tier {
			return voices[i].Name < voices[j].Name
		}
		return voices[i].Tier < voices[j].Tier
	})
	return voices, nil
}

func (p *PiperTTS) generateGoogleCloud(text string, config map[string]any, settings Settings) (string, error) {
	voice := configString(config, "tts_google_cloud_voice")
	if voice == "" {
		voice = settings.GoogleCloudVoice
	}
	if voice == "" {
		return "", fmt.Errorf("Google Cloud TTS voice is not selected")
	}
	language := configString(config, "tts_google_cloud_language")
	if language == "" {
		language = settings.GoogleCloudLanguage
	}
	if language == "" {
		parts := strings.Split(voice, "-")
		if len(parts) >= 2 {
			language = parts[0] + "-" + parts[1]
		}
	}
	rate := configFloat(config, "tts_google_cloud_speaking_rate")
	if rate == 0 {
		rate = 1.0
	}
	if rate < 0.25 || rate > 4 {
		rate = 1.0
	}
	tier, _ := googleCloudVoiceTier(voice)

	cacheKey := "google-cloud\x00" + voice + "\x00" + language + "\x00" + fmt.Sprintf("%.3f", rate) + "\x00" + text
	filename := "tts_" + sha256Short(cacheKey) + ".ogg"
	outPath := filepath.Join(p.AudioDir, filename)
	if fileExists(outPath) {
		return filename, nil
	}
	if err := os.MkdirAll(p.AudioDir, 0755); err != nil {
		return "", err
	}

	audioConfig := map[string]any{"audioEncoding": "LINEAR16"}
	if tier != "chirp3_hd" {
		audioConfig["speakingRate"] = rate
	}
	body := map[string]any{
		"input":       map[string]any{"text": text},
		"voice":       map[string]any{"languageCode": language, "name": voice},
		"audioConfig": audioConfig,
	}
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	token, err := p.googleCloudToken(ctx)
	if err != nil {
		return "", err
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://texttospeech.googleapis.com/v1/text:synthesize", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Google Cloud TTS returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed googleCloudSynthesizeResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.AudioContent == "" {
		return "", fmt.Errorf("Google Cloud TTS response did not contain audio")
	}
	wav, err := base64.StdEncoding.DecodeString(parsed.AudioContent)
	if err != nil {
		return "", err
	}
	wavPath := outPath + ".tmp.wav"
	tmpOgg := outPath + ".tmp.ogg"
	defer func() { _ = os.Remove(wavPath); _ = os.Remove(tmpOgg) }()
	if err := os.WriteFile(wavPath, wav, 0600); err != nil {
		return "", err
	}
	cmd := exec.Command("ffmpeg", "-y", "-i", wavPath, "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k", "-application", "audio", "-vn", tmpOgg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Google Cloud TTS audio conversion failed: %w (stderr: %s)", err, stderr.String())
	}
	if err := os.Rename(tmpOgg, outPath); err != nil {
		return "", err
	}
	return filename, nil
}
