from pathlib import Path

ivr = Path("internal/calling/ivr.go")
s = ivr.read_text()
replacements = [
    (
        'outcome = m.executeGreeting(session, node, player)',
        'outcome = m.executeGreeting(session, node, ctx, player)',
    ),
    (
        'func (m *Manager) executeGreeting(session *CallSession, node *IVRNode, player *AudioPlayer) string {\n\taudioFile, _ := node.Config["audio_file"].(string)',
        'func (m *Manager) executeGreeting(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {\n\taudioFile := m.resolveNodeAudioFile(node, ctx)',
    ),
    (
        'func (m *Manager) executeMenu(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {\n\taudioFile, _ := node.Config["audio_file"].(string)',
        'func (m *Manager) executeMenu(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {\n\taudioFile := m.resolveNodeAudioFile(node, ctx)',
    ),
    (
        'func (m *Manager) executeGather(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {\n\taudioFile, _ := node.Config["audio_file"].(string)',
        'func (m *Manager) executeGather(session *CallSession, node *IVRNode, ctx *IVRContext, player *AudioPlayer) string {\n\taudioFile := m.resolveNodeAudioFile(node, ctx)',
    ),
    (
        'func (m *Manager) executeHangup(session *CallSession, node *IVRNode, ctx *IVRContext, waAccount *whatsapp.Account, player *AudioPlayer) {\n\taudioFile, _ := node.Config["audio_file"].(string)',
        'func (m *Manager) executeHangup(session *CallSession, node *IVRNode, ctx *IVRContext, waAccount *whatsapp.Account, player *AudioPlayer) {\n\taudioFile := m.resolveNodeAudioFile(node, ctx)',
    ),
]
for old, new in replacements:
    if old not in s:
        raise SystemExit(f"Expected ivr.go snippet not found: {old[:80]!r}")
    s = s.replace(old, new, 1)
ivr.write_text(s)

Path("internal/calling/runtime_tts.go").write_text(r'''package calling

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
''')

main = Path("cmd/whatomate/main.go")
s = main.read_text()
old = '''\t\tapp.TTS = &tts.PiperTTS{\n\t\t\tBinaryPath:    cfg.TTS.PiperBinary,\n\t\t\tModelPath:     cfg.TTS.PiperModel,\n\t\t\tOpusencBinary: cfg.TTS.OpusencBinary,\n\t\t\tAudioDir:      cfg.Calling.AudioDir,\n\t\t}\n\t\tlo.Info("TTS initialized", "piper", cfg.TTS.PiperBinary, "model", cfg.TTS.PiperModel)'''
new = '''\t\tapp.TTS = &tts.PiperTTS{\n\t\t\tBinaryPath:    cfg.TTS.PiperBinary,\n\t\t\tModelPath:     cfg.TTS.PiperModel,\n\t\t\tOpusencBinary: cfg.TTS.OpusencBinary,\n\t\t\tAudioDir:      cfg.Calling.AudioDir,\n\t\t}\n\t\tcalling.SetRuntimeTTSGenerator(app.TTS)\n\t\tlo.Info("TTS initialized", "piper", cfg.TTS.PiperBinary, "model", cfg.TTS.PiperModel)'''
if old not in s:
    raise SystemExit("Expected main.go TTS initialization snippet not found")
main.write_text(s.replace(old, new, 1))

handlers = Path("internal/handlers/ivr_flows.go")
s = handlers.read_text()
old = '''\t\tgreetingText, _ := config["greeting_text"].(string)\n\t\tif greetingText == "" {\n\t\t\tcontinue\n\t\t}\n\t\tfilename, err := a.TTS.Generate(greetingText)'''
new = '''\t\tgreetingText, _ := config["greeting_text"].(string)\n\t\tif greetingText == "" {\n\t\t\tcontinue\n\t\t}\n\t\t// Dynamic prompts depend on Gather/HTTP values that only exist while a\n\t\t// call is running. Do not render placeholders literally at save time.\n\t\tif strings.Contains(greetingText, "{{") {\n\t\t\tdelete(config, "audio_file")\n\t\t\tnodeMap["config"] = config\n\t\t\tnodesSlice[i] = nodeMap\n\t\t\tcontinue\n\t\t}\n\t\tfilename, err := a.TTS.Generate(greetingText)'''
if old not in s:
    raise SystemExit("Expected ivr_flows.go TTS generation snippet not found")
handlers.write_text(s.replace(old, new, 1))
