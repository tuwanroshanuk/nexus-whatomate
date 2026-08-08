#!/usr/bin/env python3
"""Low-memory Coqui-VITS ONNX inference helper for Whatomate.

This runner intentionally depends only on numpy + onnxruntime. It is started
only for an uncached custom VITS request and exits immediately afterwards, so
there is no model-sized resident worker in the application process.
"""

import argparse
import json
import math
import os
import sys
import wave
from pathlib import Path

import numpy as np
import onnxruntime as ort


# Sinhala -> romanized text mapping compatible with the Roshan Sinhala VITS
# training convention. Implemented as Unicode character rules rather than by
# importing/executing a downloaded romanizer.py.
VOWELS = {
    "අ": "a", "ආ": "ā", "ඇ": "æ", "ඈ": "ǣ", "ඉ": "i", "ඊ": "ī",
    "උ": "u", "ඌ": "ū", "ඍ": "ṛ", "ඎ": "ṝ", "ඐ": "ḹ", "එ": "e",
    "ඒ": "ē", "ඓ": "ai", "ඔ": "o", "ඕ": "ō", "ඖ": "au",
}

CONSONANTS = {
    "ක": "k", "ඛ": "kh", "ග": "g", "ඝ": "gh", "ඞ": "ṅ", "ඟ": "ṉg",
    "ච": "c", "ඡ": "ch", "ජ": "j", "ඣ": "jh", "ඤ": "ñ", "ඥ": "gn", "ඦ": "ñj",
    "ට": "ṭ", "ඨ": "ṭh", "ඩ": "ḍ", "ඪ": "ḍh", "ණ": "ṇ", "ඬ": "ṇḍ",
    "ත": "t", "ථ": "th", "ද": "d", "ධ": "dh", "න": "n", "ඳ": "ṉd",
    "ප": "p", "ඵ": "ph", "බ": "b", "භ": "bh", "ම": "m", "ඹ": "mb",
    "ය": "y", "ර": "r", "ල": "l", "ව": "v", "ශ": "ś", "ෂ": "ṣ",
    "ස": "s", "හ": "h", "ළ": "ḷ", "ෆ": "f",
}

VOWEL_SIGNS = {
    "ා": "ā", "ැ": "æ", "ෑ": "ǣ", "ි": "i", "ී": "ī", "ු": "u",
    "ූ": "ū", "ෘ": "ṛ", "ෲ": "ṝ", "ෙ": "e", "ේ": "ē", "ෛ": "ai",
    "ො": "o", "ෝ": "ō", "ෞ": "au", "ෳ": "ḹ",
}

SPECIALS = {"ං": "ṁ", "ඃ": "ḥ"}
VIRAMA = "්"
ZWJ = "\u200d"

# The converted model's vocabulary does not contain ASCII digits. Spelling
# digits as Sinhala words keeps phone/account-number TTS usable after the
# existing Whatomate number-mode formatting has separated them.
DIGIT_WORDS = {
    "0": "බිංදුව", "1": "එක", "2": "දෙක", "3": "තුන", "4": "හතර",
    "5": "පහ", "6": "හය", "7": "හත", "8": "අට", "9": "නවය",
}


def expand_digits(text: str) -> str:
    return "".join(DIGIT_WORDS.get(ch, ch) for ch in text)


def sinhala_to_roman(text: str) -> str:
    text = expand_digits(text.replace(ZWJ, ""))
    out = []
    i = 0
    while i < len(text):
        ch = text[i]
        if ch in CONSONANTS:
            base = CONSONANTS[ch]
            if i + 1 < len(text):
                nxt = text[i + 1]
                if nxt == VIRAMA:
                    out.append(base)
                    i += 2
                    continue
                if nxt in VOWEL_SIGNS:
                    out.append(base + VOWEL_SIGNS[nxt])
                    i += 2
                    continue
            out.append(base + "a")
            i += 1
            continue
        if ch in VOWELS:
            out.append(VOWELS[ch])
        elif ch in SPECIALS:
            out.append(SPECIALS[ch])
        elif ch == VIRAMA:
            # A virama not consumed with a consonant carries no sound.
            pass
        else:
            out.append(ch)
        i += 1
    return "".join(out)


def load_config(path: Path):
    with path.open("r", encoding="utf-8") as f:
        cfg = json.load(f)
    token_map = cfg.get("phoneme_id_map")
    if not isinstance(token_map, dict) or not token_map:
        raise RuntimeError("custom VITS config is missing phoneme_id_map")
    normalized = {}
    for token, raw_ids in token_map.items():
        if isinstance(raw_ids, list):
            ids = [int(v) for v in raw_ids]
        else:
            ids = [int(raw_ids)]
        if ids:
            normalized[token] = ids
    if not normalized:
        raise RuntimeError("custom VITS phoneme_id_map contains no usable IDs")
    return cfg, normalized


def encode_text(text: str, token_map):
    # Longest-token-first also supports maps that contain multi-character
    # romanized units, while ordinary character maps remain a fast path.
    tokens = sorted(token_map, key=len, reverse=True)
    ids = []
    i = 0
    while i < len(text):
        matched = False
        for token in tokens:
            if text.startswith(token, i):
                ids.extend(token_map[token])
                i += len(token)
                matched = True
                break
        if matched:
            continue
        ch = text[i]
        # Upper-case input is not part of this voice's vocabulary; lower-case
        # when the corresponding token exists. Ignore only zero-width chars.
        lower = ch.lower()
        if lower != ch and lower in token_map:
            ids.extend(token_map[lower])
            i += 1
            continue
        if ch in (ZWJ, "\ufeff"):
            i += 1
            continue
        raise RuntimeError(f"character {ch!r} is not present in the VITS tokenizer map")
    if not ids:
        raise RuntimeError("text produced no VITS token IDs")
    return ids


def write_pcm16_wav(path: Path, audio: np.ndarray, sample_rate: int):
    audio = np.asarray(audio, dtype=np.float32).squeeze()
    if audio.ndim != 1:
        audio = audio.reshape(-1)
    if audio.size == 0 or not np.isfinite(audio).all():
        raise RuntimeError("VITS ONNX returned invalid audio")
    peak = float(np.max(np.abs(audio)))
    if peak > 1.0:
        audio = audio / peak * 0.95
    pcm = np.clip(audio, -1.0, 1.0)
    pcm = (pcm * 32767.0).astype("<i2")
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(sample_rate)
        wf.writeframes(pcm.tobytes())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True)
    parser.add_argument("--config", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--length-scale", type=float, default=1.0)
    args = parser.parse_args()

    text = sys.stdin.read().strip()
    if not text:
        raise RuntimeError("TTS text is empty")

    model_path = Path(args.model)
    config_path = Path(args.config)
    output_path = Path(args.output)
    cfg, token_map = load_config(config_path)

    roman = sinhala_to_roman(text)
    ids = encode_text(roman, token_map)

    inference = cfg.get("inference") or {}
    noise_scale = float(inference.get("noise_scale", 0.667))
    noise_w = float(inference.get("noise_w", 0.8))
    length_scale = min(2.0, max(0.5, float(args.length_scale)))
    sample_rate = int((cfg.get("audio") or {}).get("sample_rate", 22050))

    # Low-memory CPU session. Disabling the arena and memory pattern reduces
    # transient/reserved RAM. The process exits after synthesis, releasing the
    # model completely. Two threads keeps IVR latency good without monopolizing
    # the host CPU.
    so = ort.SessionOptions()
    so.enable_cpu_mem_arena = False
    so.enable_mem_pattern = False
    so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_EXTENDED
    so.intra_op_num_threads = max(1, min(2, os.cpu_count() or 1))
    so.inter_op_num_threads = 1

    session = ort.InferenceSession(str(model_path), sess_options=so, providers=["CPUExecutionProvider"])
    input_ids = np.asarray([ids], dtype=np.int64)
    input_lengths = np.asarray([len(ids)], dtype=np.int64)
    scales = np.asarray([noise_scale, length_scale, noise_w], dtype=np.float32)

    result = session.run(None, {
        "input": input_ids,
        "input_lengths": input_lengths,
        "scales": scales,
    })
    if not result:
        raise RuntimeError("VITS ONNX returned no outputs")
    output_path.parent.mkdir(parents=True, exist_ok=True)
    write_pcm16_wav(output_path, result[0], sample_rate)


if __name__ == "__main__":
    main()
