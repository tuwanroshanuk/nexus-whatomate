from pathlib import Path

p = Path('cmd/whatomate/main.go')
s = p.read_text()
old = '''\t// TTS settings and Piper voice management
\tg.GET("/api/tts/settings", app.GetTTSSettings)
\tg.PUT("/api/tts/settings", app.UpdateTTSSettings)
\tg.POST("/api/tts/models/download", app.DownloadTTSModel)
\tg.DELETE("/api/tts/models/{name}", app.UninstallTTSModel)
\tg.POST("/api/tts/preview", app.PreviewTTS)
'''
new = '''\t// TTS settings, provider credentials and voice management
\tg.GET("/api/tts/settings", app.GetTTSSettings)
\tg.PUT("/api/tts/settings", app.UpdateTTSSettings)
\tg.POST("/api/tts/models/download", app.DownloadTTSModel)
\t// Keep these three contiguous: the Cloud-provider extension inserts its
\t// routes between the Gemini test route and preview route.
\tg.POST("/api/tts/providers/gemini/test", app.TestGeminiTTSProvider)
\tg.POST("/api/tts/preview", app.PreviewTTS)
\tg.PUT("/api/tts/providers/gemini", app.UpdateGeminiTTSCredentials)
\tg.DELETE("/api/tts/providers/gemini", app.DeleteGeminiTTSCredentials)
\tg.DELETE("/api/tts/models/{name}", app.UninstallTTSModel)
'''
if old in s:
    s = s.replace(old, new, 1)
elif 'app.TestGeminiTTSProvider' not in s:
    raise SystemExit('TTS route block not found')
p.write_text(s)
print('TTS provider routes normalized')
