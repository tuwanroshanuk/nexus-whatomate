#!/bin/sh
set -eu

REAL_PIPER="${WHATOMATE_PIPER_REAL:-/usr/local/bin/piper-real}"
VITS_PYTHON="${WHATOMATE_VITS_PYTHON:-/opt/whatomate-vits/bin/python3}"
VITS_RUNNER="${WHATOMATE_VITS_RUNNER:-/usr/local/lib/whatomate/vits_onnx.py}"
VITS_LOCK="${WHATOMATE_VITS_LOCK:-/tmp/whatomate-vits.lock}"

model=""
output=""
length_scale="1.0"

# Keep Piper CLI compatibility while extracting only the arguments required by
# the custom VITS runner. Unknown options are left for real Piper.
prev=""
for arg in "$@"; do
    case "$prev" in
        model) model="$arg"; prev=""; continue ;;
        output) output="$arg"; prev=""; continue ;;
        length) length_scale="$arg"; prev=""; continue ;;
    esac
    case "$arg" in
        --model) prev="model" ;;
        --output_file|--output-file) prev="output" ;;
        --length_scale|--length-scale) prev="length" ;;
    esac
done

config="${model}.json"

# Converted Coqui VITS packages carry explicit source metadata. Route only
# those models; every native Piper voice still goes through the real binary.
if [ -n "$model" ] && [ -n "$output" ] && [ -f "$config" ] \
   && grep -q '"framework"[[:space:]]*:[[:space:]]*"Coqui VITS"' "$config" \
   && grep -q '"requires_external_romanizer"[[:space:]]*:[[:space:]]*true' "$config"; then
    # Only one model-sized ONNX process is allowed at a time. Waiting calls stay
    # as tiny shell processes, preventing concurrent IVR requests from
    # multiplying the ~100+ MB model working set.
    while ! mkdir "$VITS_LOCK" 2>/dev/null; do
        sleep 0.05
    done
    trap 'rmdir "$VITS_LOCK" 2>/dev/null || true' EXIT INT TERM

    "$VITS_PYTHON" "$VITS_RUNNER" \
        --model "$model" \
        --config "$config" \
        --output "$output" \
        --length-scale "$length_scale"
    status=$?
    rmdir "$VITS_LOCK" 2>/dev/null || true
    trap - EXIT INT TERM
    exit "$status"
fi

exec "$REAL_PIPER" "$@"
