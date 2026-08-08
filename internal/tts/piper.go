package tts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	NumberModeNatural     = "natural"
	NumberModePhoneDigits = "phone_digits"
	NumberModeAllDigits   = "all_digits"
)

// Settings are server-wide Piper defaults. Individual IVR TTS nodes can
// override these values without changing the global defaults.
type Settings struct {
	DefaultModel    string  `json:"default_model"`
	DefaultLanguage string  `json:"default_language"`
	NumberMode      string  `json:"number_mode"`
	LengthScale     float64 `json:"length_scale"`
}

// ModelInfo describes one installed Piper .onnx voice.
type ModelInfo struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Language  string `json:"language"`
	Size      int64  `json:"size"`
	HasConfig bool   `json:"has_config"`
	IsDefault bool   `json:"is_default"`
}

// PiperTTS generates OGG/Opus audio files from text using the Piper TTS CLI
// and opusenc. ModelDir can contain multiple .onnx voices; SettingsPath is
// optional and defaults to <ModelDir>/.whatomate-tts-settings.json.
type PiperTTS struct {
	BinaryPath    string
	ModelPath     string
	ModelDir      string
	SettingsPath  string
	OpusencBinary string
	AudioDir      string
	mu            sync.RWMutex
}

func (p *PiperTTS) modelDir() string {
	if strings.TrimSpace(p.ModelDir) != "" {
		return p.ModelDir
	}
	if strings.TrimSpace(p.ModelPath) != "" {
		return filepath.Dir(p.ModelPath)
	}
	return "./tts-models"
}

func (p *PiperTTS) settingsPath() string {
	if strings.TrimSpace(p.SettingsPath) != "" {
		return p.SettingsPath
	}
	return filepath.Join(p.modelDir(), ".whatomate-tts-settings.json")
}

func validNumberMode(mode string) bool {
	switch mode {
	case NumberModeNatural, NumberModePhoneDigits, NumberModeAllDigits:
		return true
	default:
		return false
	}
}

func (p *PiperTTS) defaultSettings() Settings {
	model := filepath.Base(p.ModelPath)
	if model == "." {
		model = ""
	}
	return Settings{
		DefaultModel:    model,
		DefaultLanguage: "auto",
		NumberMode:      NumberModePhoneDigits,
		LengthScale:     1.0,
	}
}

func (p *PiperTTS) GetSettings() Settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	settings := p.defaultSettings()
	data, err := os.ReadFile(p.settingsPath())
	if err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if !validNumberMode(settings.NumberMode) {
		settings.NumberMode = NumberModePhoneDigits
	}
	if settings.LengthScale < 0.5 || settings.LengthScale > 2.0 {
		settings.LengthScale = 1.0
	}
	if settings.DefaultLanguage == "" {
		settings.DefaultLanguage = "auto"
	}
	return settings
}

func (p *PiperTTS) UpdateSettings(settings Settings) (Settings, error) {
	if !validNumberMode(settings.NumberMode) {
		return Settings{}, fmt.Errorf("invalid number pronunciation mode")
	}
	if settings.LengthScale < 0.5 || settings.LengthScale > 2.0 {
		return Settings{}, fmt.Errorf("length_scale must be between 0.5 and 2.0")
	}
	if settings.DefaultLanguage == "" {
		settings.DefaultLanguage = "auto"
	}
	if settings.DefaultModel != "" {
		if _, err := p.resolveModel(settings.DefaultModel); err != nil {
			return Settings{}, err
		}
	}
	if err := os.MkdirAll(p.modelDir(), 0755); err != nil {
		return Settings{}, err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return Settings{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	tmp := p.settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return Settings{}, err
	}
	if err := os.Rename(tmp, p.settingsPath()); err != nil {
		_ = os.Remove(tmp)
		return Settings{}, err
	}
	return settings, nil
}

func (p *PiperTTS) ListModels() ([]ModelInfo, error) {
	dir := p.modelDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	settings := p.GetSettings()
	models := make([]ModelInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".onnx") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		model := ModelInfo{
			File:      entry.Name(),
			Name:      strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Size:      info.Size(),
			HasConfig: fileExists(filepath.Join(dir, entry.Name()+".json")),
			IsDefault: entry.Name() == settings.DefaultModel,
		}
		model.Language = readModelLanguage(filepath.Join(dir, entry.Name()+".json"))
		models = append(models, model)
	}
	// The configured legacy/default model may be outside ModelDir. Keep it
	// selectable so an existing deployment is not broken when multi-model mode
	// is enabled.
	if p.ModelPath != "" && fileExists(p.ModelPath) {
		base := filepath.Base(p.ModelPath)
		found := false
		for _, model := range models {
			if model.File == base {
				found = true
				break
			}
		}
		if !found {
			info, _ := os.Stat(p.ModelPath)
			models = append(models, ModelInfo{
				File: base, Name: strings.TrimSuffix(base, filepath.Ext(base)),
				Language: readModelLanguage(p.ModelPath + ".json"),
				Size:     info.Size(), HasConfig: fileExists(p.ModelPath + ".json"),
				IsDefault: base == settings.DefaultModel,
			})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

func readModelLanguage(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return ""
	}
	if language, ok := raw["language"].(map[string]any); ok {
		if code, ok := language["code"].(string); ok {
			return code
		}
	}
	return ""
}

func sanitizeModelFilename(name string) (string, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid model name")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".onnx") {
		name += ".onnx"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(name) {
		return "", fmt.Errorf("model name contains unsupported characters")
	}
	return name, nil
}

// InstallModel atomically installs a Piper .onnx model and optional matching
// .onnx.json configuration into ModelDir.
func (p *PiperTTS) InstallModel(name string, modelData, configData []byte) (ModelInfo, error) {
	filename, err := sanitizeModelFilename(name)
	if err != nil {
		return ModelInfo{}, err
	}
	if len(modelData) == 0 {
		return ModelInfo{}, fmt.Errorf("model data is empty")
	}
	dir := p.modelDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ModelInfo{}, err
	}
	modelPath := filepath.Join(dir, filename)
	modelTmp := modelPath + ".download"
	if err := os.WriteFile(modelTmp, modelData, 0644); err != nil {
		return ModelInfo{}, err
	}
	if err := os.Rename(modelTmp, modelPath); err != nil {
		_ = os.Remove(modelTmp)
		return ModelInfo{}, err
	}
	if len(configData) > 0 {
		var verify any
		if err := json.Unmarshal(configData, &verify); err != nil {
			_ = os.Remove(modelPath)
			return ModelInfo{}, fmt.Errorf("model config is not valid JSON: %w", err)
		}
		configPath := modelPath + ".json"
		configTmp := configPath + ".download"
		if err := os.WriteFile(configTmp, configData, 0644); err != nil {
			return ModelInfo{}, err
		}
		if err := os.Rename(configTmp, configPath); err != nil {
			_ = os.Remove(configTmp)
			return ModelInfo{}, err
		}
	}
	info, _ := os.Stat(modelPath)
	return ModelInfo{
		File: filename, Name: strings.TrimSuffix(filename, filepath.Ext(filename)),
		Language: readModelLanguage(modelPath + ".json"), Size: info.Size(),
		HasConfig: fileExists(modelPath + ".json"),
	}, nil
}

// UninstallModel removes an installed Piper model and its matching JSON config.
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

func (p *PiperTTS) resolveModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		settings := p.GetSettings()
		model = settings.DefaultModel
	}
	if model == "" && p.ModelPath != "" {
		return p.ModelPath, nil
	}
	base := filepath.Base(model)
	candidate := filepath.Join(p.modelDir(), base)
	if fileExists(candidate) {
		return candidate, nil
	}
	if p.ModelPath != "" && filepath.Base(p.ModelPath) == base && fileExists(p.ModelPath) {
		return p.ModelPath, nil
	}
	return "", fmt.Errorf("Piper model %q is not installed", model)
}

func configString(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	if value, ok := config[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func configFloat(config map[string]any, key string) float64 {
	if config == nil {
		return 0
	}
	switch value := config[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case string:
		parsed, _ := strconv.ParseFloat(value, 64)
		return parsed
	default:
		return 0
	}
}

// PrepareText applies the configured number-speaking mode. "phone_digits"
// only expands 7–15 digit sequences (E.164-style phone/account numbers), while
// "all_digits" expands every number. Comma-separated single digits allow the
// selected Piper language model to pronounce each digit in its own language.
func PrepareText(text, mode string) string {
	if !validNumberMode(mode) || mode == NumberModeNatural {
		return text
	}
	pattern := `\d+`
	if mode == NumberModePhoneDigits {
		pattern = `\d{7,15}`
	}
	re := regexp.MustCompile(pattern)
	return re.ReplaceAllStringFunc(text, func(value string) string {
		parts := make([]string, 0, len(value))
		for _, digit := range value {
			parts = append(parts, string(digit))
		}
		return strings.Join(parts, ", ")
	})
}

// Generate keeps backward compatibility and uses global defaults.
func (p *PiperTTS) Generate(text string) (string, error) {
	return p.GenerateWithConfig(text, nil)
}

// GenerateWithConfig resolves per-node overrides on top of global defaults.
// Supported config keys: tts_model, tts_language, tts_number_mode,
// tts_length_scale. tts_language is metadata for the editor; actual speech
// language is determined by the selected Piper model.
func (p *PiperTTS) GenerateWithConfig(text string, config map[string]any) (string, error) {
	settings := p.GetSettings()
	model := configString(config, "tts_model")
	if model == "" {
		model = settings.DefaultModel
	}
	mode := configString(config, "tts_number_mode")
	if mode == "" {
		mode = settings.NumberMode
	}
	lengthScale := configFloat(config, "tts_length_scale")
	if lengthScale == 0 {
		lengthScale = settings.LengthScale
	}
	if lengthScale < 0.5 || lengthScale > 2.0 {
		lengthScale = 1.0
	}
	modelPath, err := p.resolveModel(model)
	if err != nil {
		return "", err
	}
	prepared := PrepareText(text, mode)
	cacheKey := prepared + "\x00" + modelPath + "\x00" + fmt.Sprintf("%.3f", lengthScale)
	hash := sha256Short(cacheKey)
	filename := "tts_" + hash + ".ogg"
	outPath := filepath.Join(p.AudioDir, filename)
	if fileExists(outPath) {
		return filename, nil
	}
	if err := os.MkdirAll(p.AudioDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create audio directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wavPath := outPath + ".tmp.wav"
	defer func() { _ = os.Remove(wavPath) }()
	piperCmd := exec.CommandContext(ctx, p.BinaryPath,
		"--model", modelPath,
		"--output_file", wavPath,
		"--length_scale", strconv.FormatFloat(lengthScale, 'f', 3, 64),
	)
	piperCmd.Stdin = bytes.NewReader([]byte(prepared))
	var piperStderr bytes.Buffer
	piperCmd.Stderr = &piperStderr
	if err := piperCmd.Run(); err != nil {
		return "", fmt.Errorf("piper TTS failed: %w (stderr: %s)", err, piperStderr.String())
	}
	if !fileExists(wavPath) {
		return "", fmt.Errorf("piper did not produce output file")
	}
	opusenc := p.OpusencBinary
	if opusenc == "" {
		opusenc = "opusenc"
	}
	tmpOgg := outPath + ".tmp.ogg"
	defer func() { _ = os.Remove(tmpOgg) }()
	encCmd := exec.CommandContext(ctx, opusenc,
		"--bitrate", "24", "--quiet", wavPath, tmpOgg,
	)
	var encStderr bytes.Buffer
	encCmd.Stderr = &encStderr
	if err := encCmd.Run(); err != nil {
		return "", fmt.Errorf("opusenc failed: %w (stderr: %s)", err, encStderr.String())
	}
	if err := os.Rename(tmpOgg, outPath); err != nil {
		return "", fmt.Errorf("failed to finalize audio file: %w", err)
	}
	return filename, nil
}

func sha256Short(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
