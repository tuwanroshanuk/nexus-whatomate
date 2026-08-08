from pathlib import Path

Path("frontend/src/components/settings").mkdir(parents=True, exist_ok=True)
script_path = Path(".github/scripts/apply_tts_settings.py")
source = script_path.read_text()
needle = '''    if old not in s:\n        raise SystemExit(f"missing expected snippet in {path}: {old[:120]!r}")\n    p.write_text(s.replace(old, new, 1))'''
replacement = '''    if old not in s:\n        trimmed = old.rstrip()\n        if trimmed in s:\n            old = trimmed\n        else:\n            raise SystemExit(f"missing expected snippet in {path}: {old[:120]!r}")\n    p.write_text(s.replace(old, new, 1))'''
if needle not in source:
    raise SystemExit("could not patch replace_once helper")
source = source.replace(needle, replacement, 1)
exec(compile(source, str(script_path), "exec"), {"__name__": "__main__", "__file__": str(script_path)})
