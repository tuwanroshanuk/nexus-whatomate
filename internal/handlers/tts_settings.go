package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/tts"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func (a *App) requireTTS(r *fastglue.Request) bool {
	if a.TTS == nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Text-to-speech is not configured on this server", nil, "")
		return false
	}
	return true
}

// GetTTSSettings returns server-wide defaults plus installed Piper models.
func (a *App) GetTTSSettings(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	modelsList, err := a.TTS.ListModels()
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list TTS models: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{
		"settings":  a.TTS.GetSettings(),
		"models":    modelsList,
		"model_dir": a.Config.TTS.ModelDir,
		"providers": a.TTS.GetProviderStatus(),
	})
}

// UpdateTTSSettings updates global defaults. Individual IVR nodes may override
// these settings in the Flow Builder.
func (a *App) UpdateTTSSettings(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	var req tts.Settings
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	settings, err := a.TTS.UpdateSettings(req)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	return r.SendEnvelope(settings)
}

type downloadTTSModelRequest struct {
	Name      string `json:"name"`
	ModelURL  string `json:"model_url"`
	ConfigURL string `json:"config_url"`
}

func validateTTSDownloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return nil, fmt.Errorf("only valid HTTPS download URLs are allowed")
	}
	return u, nil
}

func (a *App) downloadTTSAsset(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	u, err := validateTTSDownloadURL(rawURL)
	if err != nil {
		return nil, err
	}
	if a.HTTPClient == nil {
		return nil, fmt.Errorf("HTTP client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("download is larger than the allowed limit")
	}
	return data, nil
}

// DownloadTTSModel downloads a Piper ONNX model and its JSON config into the
// configured model directory. The shared HTTP client applies the application's
// outbound-request protections; downloads are also HTTPS-only and size-limited.
func (a *App) DownloadTTSModel(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	var req downloadTTSModelRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ModelURL) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Model name and model URL are required", nil, "")
	}
	ctx, cancel := context.WithTimeout(r.RequestCtx, 5*time.Minute)
	defer cancel()
	modelData, err := a.downloadTTSAsset(ctx, req.ModelURL, 500<<20)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Model download failed: "+err.Error(), nil, "")
	}
	var configData []byte
	if strings.TrimSpace(req.ConfigURL) != "" {
		configData, err = a.downloadTTSAsset(ctx, req.ConfigURL, 10<<20)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Model config download failed: "+err.Error(), nil, "")
		}
	}
	model, err := a.TTS.InstallModel(req.Name, modelData, configData)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Could not install TTS model: "+err.Error(), nil, "")
	}
	a.Log.Info("TTS model installed", "model", model.File, "size", model.Size)
	return r.SendEnvelope(model)
}

// UninstallTTSModel removes a managed Piper model and its matching config file.
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

type ttsPreviewRequest struct {
	Text                    string  `json:"text"`
	Provider                string  `json:"provider"`
	GeminiModel             string  `json:"gemini_model"`
	GeminiVoice             string  `json:"gemini_voice"`
	GeminiPrompt            string  `json:"gemini_prompt"`
	GoogleCloudVoice        string  `json:"google_cloud_voice"`
	GoogleCloudLanguage     string  `json:"google_cloud_language"`
	GoogleCloudSpeakingRate float64 `json:"google_cloud_speaking_rate"`
	Model                   string  `json:"model"`
	Language                string  `json:"language"`
	NumberMode              string  `json:"number_mode"`
	LengthScale             float64 `json:"length_scale"`
}

// PreviewTTS generates an audio preview using the same option path as IVR.
func (a *App) PreviewTTS(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	var req ttsPreviewRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if strings.TrimSpace(req.Text) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Preview text is required", nil, "")
	}
	if len([]rune(req.Text)) > 500 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Preview text is limited to 500 characters", nil, "")
	}
	config := map[string]any{
		"tts_provider":                   req.Provider,
		"tts_gemini_model":               req.GeminiModel,
		"tts_gemini_voice":               req.GeminiVoice,
		"tts_gemini_prompt":              req.GeminiPrompt,
		"tts_google_cloud_voice":         req.GoogleCloudVoice,
		"tts_google_cloud_language":      req.GoogleCloudLanguage,
		"tts_google_cloud_speaking_rate": req.GoogleCloudSpeakingRate,
		"tts_model":                      req.Model,
		"tts_language":                   req.Language,
		"tts_number_mode":                req.NumberMode,
		"tts_length_scale":               req.LengthScale,
	}
	filename, err := a.TTS.GenerateWithConfig(req.Text, config)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "TTS preview failed: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]string{"filename": filename})
}
