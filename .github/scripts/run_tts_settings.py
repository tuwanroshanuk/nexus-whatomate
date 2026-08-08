from pathlib import Path
import runpy

Path("frontend/src/components/settings").mkdir(parents=True, exist_ok=True)
runpy.run_path(".github/scripts/apply_tts_settings.py", run_name="__main__")
