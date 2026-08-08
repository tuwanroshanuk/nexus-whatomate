package calling

import "strings"

// RuntimeTTSGenerator is the minimal TTS contract needed by the live IVR
// engine. The application registers its configured Piper generator at startup.
type RuntimeTTSGenerator interface {
	Generate(text string) (string, error)
}

var runtimeTTSGenerator RuntimeTTSGenerator

// SetRuntimeTTSGenerator makes the configured TTS engine available to live IVR
// nodes. It is called once during application startup before calls are handled.
func SetRuntimeTTSGenerator(generator RuntimeTTSGenerator) {
	runtimeTTSGenerator = generator
}

// resolveNodeAudioFile returns pre-generated audio for static prompts. For
// prompts containing {{variable}} placeholders, it resolves the current call's
// IVR context and generates/caches audio from the resolved text at runtime.
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

	filename, err := runtimeTTSGenerator.Generate(resolved)
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
