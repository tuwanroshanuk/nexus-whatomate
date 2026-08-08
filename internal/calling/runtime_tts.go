package calling

import "strings"

// RuntimeTTSGenerator is the TTS contract used by the live IVR engine. Node
// configuration is passed through so model, number pronunciation and speed can
// be overridden per TTS-capable node.
type RuntimeTTSGenerator interface {
	Generate(text string) (string, error)
	GenerateWithConfig(text string, config map[string]any) (string, error)
}

var runtimeTTSGenerator RuntimeTTSGenerator

func SetRuntimeTTSGenerator(generator RuntimeTTSGenerator) {
	runtimeTTSGenerator = generator
}

// resolveNodeAudioFile returns pre-generated audio for static prompts. Dynamic
// prompts are resolved against the live IVR variables, then generated using the
// node's TTS options. Static prompts are regenerated on flow save whenever TTS
// options change, so they already point at the correctly cached audio file.
func (m *Manager) resolveNodeAudioFile(node *IVRNode, ctx *IVRContext) string {
	audioFile, _ := node.Config["audio_file"].(string)
	greetingText, _ := node.Config["greeting_text"].(string)

	if greetingText == "" || !strings.Contains(greetingText, "{{") {
		return audioFile
	}
	if ctx == nil {
		m.log.Warn("Dynamic IVR TTS has no runtime context", "node_id", node.ID)
		return audioFile
	}

	resolved := interpolateTemplate(greetingText, ctx.Variables)
	if strings.Contains(resolved, "{{") {
		m.log.Warn("Dynamic IVR TTS contains unresolved variables", "node_id", node.ID)
		return audioFile
	}
	if runtimeTTSGenerator == nil {
		m.log.Error("Dynamic IVR TTS requested but no runtime TTS generator is configured", "node_id", node.ID)
		return audioFile
	}

	filename, err := runtimeTTSGenerator.GenerateWithConfig(resolved, node.Config)
	if err != nil {
		m.log.Error("Dynamic IVR TTS generation failed", "error", err, "node_id", node.ID)
		return audioFile
	}
	if filename == "" {
		m.log.Error("Dynamic IVR TTS generator returned an empty filename", "node_id", node.ID)
		return audioFile
	}
	return filename
}
