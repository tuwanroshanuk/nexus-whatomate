package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type geminiCredentialRequest struct {
	APIKey string `json:"api_key"`
}

func (a *App) UpdateGeminiTTSCredentials(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	var req geminiCredentialRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
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
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
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
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	settings := a.TTS.GetSettings()
	filename, err := a.TTS.GenerateWithConfig("Hello. Gemini text to speech is connected successfully.", map[string]any{
		"tts_provider":     "gemini",
		"tts_gemini_model": settings.GeminiModel,
		"tts_gemini_voice": settings.GeminiVoice,
	})
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Gemini TTS test failed: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true, "filename": filename})
}

type googleCloudCredentialRequest struct {
	ServiceAccountJSON string `json:"service_account_json"`
}

func (a *App) UpdateGoogleCloudTTSCredentials(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	var req googleCloudCredentialRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if err := a.TTS.SetGoogleCloudCredentials(req.ServiceAccountJSON); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	a.Log.Info("Google Cloud TTS credentials updated")
	status := a.TTS.GetProviderStatus()
	return r.SendEnvelope(status.GoogleCloud)
}

func (a *App) DeleteGoogleCloudTTSCredentials(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionWrite); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	if a.TTS.GetSettings().DefaultProvider == "google_cloud" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Switch the default TTS provider away from Google Cloud before removing its credentials", nil, "")
	}
	if err := a.TTS.ClearGoogleCloudCredentials(); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Could not remove Google Cloud TTS credentials", nil, "")
	}
	return r.SendEnvelope(map[string]any{"configured": false})
}

func (a *App) ListGoogleCloudTTSVoices(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.RequestCtx, 30*time.Second)
	defer cancel()
	voices, err := a.TTS.ListGoogleCloudVoices(ctx)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not load Google Cloud TTS voices: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"voices": voices})
}

func (a *App) TestGoogleCloudTTSProvider(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceIVRFlows, models.ActionRead); err != nil {
		return nil
	}
	if !a.requireTTS(r) {
		return nil
	}
	settings := a.TTS.GetSettings()
	if strings.TrimSpace(settings.GoogleCloudVoice) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Select and save a Google Cloud voice first", nil, "")
	}
	filename, err := a.TTS.GenerateWithConfig("Hello. Google Cloud text to speech is connected successfully.", map[string]any{
		"tts_provider":              "google_cloud",
		"tts_google_cloud_voice":    settings.GoogleCloudVoice,
		"tts_google_cloud_language": settings.GoogleCloudLanguage,
	})
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Google Cloud TTS test failed: "+err.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true, "filename": filename})
}
