#!/usr/bin/env bash
# Fetches everything the real speech backend needs:
#   third_party/sherpa-onnx   prebuilt text-to-speech runtime
#   models/                   whisper, OPUS-MT and Piper voice models
#
# usage: scripts/setup-sussurro.sh [sherpa] [voices] [whisper] [translation] [converter]
# With no argument every step but converter runs, which translation runs when
# it needs to. Steps are idempotent. AMBIVOX_CONVERT_PYTHON names a Python that
# already has the converter's requirements, instead of a private venv.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODELS="${AMBIVOX_MODELS:-$ROOT/models}"
THIRD_PARTY="$ROOT/third_party"
SHERPA_VERSION="v1.13.8"
SUSSURRO_REV="298a657fc8710db844e0b82925b9e454b456fa81"
VOICES=(en_US-ryan-medium fr_FR-tom-medium es_ES-davefx-medium it_IT-paola-medium)
TRANSLATION_MODELS=(
  "Helsinki-NLP/opus-mt-tc-big-itc-itc tc-itc-itc"
  "Helsinki-NLP/opus-mt-tc-big-en-fr tc-en-fr"
  "Helsinki-NLP/opus-mt-tc-big-en-es tc-en-es"
  "Helsinki-NLP/opus-mt-tc-big-en-it tc-en-it"
  "Helsinki-NLP/opus-mt-tc-big-fr-en tc-fr-en"
  "Helsinki-NLP/opus-mt-tc-big-it-en tc-it-en"
  "Helsinki-NLP/opus-mt-es-en es-en"
)

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

setup_sherpa() {
  local dest="$THIRD_PARTY/sherpa-onnx"
  if [ -f "$dest/include/sherpa-onnx/c-api/c-api.h" ]; then
    say "sherpa-onnx already present"
    return
  fi
  local platform
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64)  platform="osx-arm64-shared" ;;
    Darwin-x86_64) platform="osx-x64-shared" ;;
    Linux-x86_64)  platform="linux-x64-shared" ;;
    Linux-aarch64) platform="linux-aarch64-shared-cpu" ;;
    *) die "no prebuilt sherpa-onnx for $(uname -s)-$(uname -m); build it and set AMBIVOX_SHERPA_DIR" ;;
  esac
  local name="sherpa-onnx-$SHERPA_VERSION-$platform"
  say "downloading $name"
  mkdir -p "$THIRD_PARTY"
  curl -fSL "https://github.com/k2-fsa/sherpa-onnx/releases/download/$SHERPA_VERSION/$name.tar.bz2" \
    | tar xj -C "$THIRD_PARTY"
  mv "$THIRD_PARTY/$name" "$dest"
  if [ ! -f "$dest/include/sherpa-onnx/c-api/c-api.h" ]; then
    mkdir -p "$dest/include/sherpa-onnx/c-api"
    curl -fsSL -o "$dest/include/sherpa-onnx/c-api/c-api.h" \
      "https://raw.githubusercontent.com/k2-fsa/sherpa-onnx/$SHERPA_VERSION/sherpa-onnx/c-api/c-api.h"
  fi
  curl -fsSL -o "$dest/LICENSE" "https://raw.githubusercontent.com/k2-fsa/sherpa-onnx/$SHERPA_VERSION/LICENSE"
  if [ "$(uname -s)" = "Darwin" ]; then
    xattr -dr com.apple.quarantine "$dest" 2>/dev/null || true
  fi
}

setup_voices() {
  mkdir -p "$MODELS"
  for voice in "${VOICES[@]}"; do
    if [ -d "$MODELS/vits-piper-$voice" ]; then
      say "voice $voice already present"
      continue
    fi
    say "downloading voice $voice"
    curl -fSL "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-$voice.tar.bz2" \
      | tar xj -C "$MODELS"
  done
}

setup_whisper() {
  mkdir -p "$MODELS"
  if [ -f "$MODELS/ggml-small.bin" ]; then
    say "whisper model already present"
    return
  fi
  say "downloading whisper small"
  curl -fSL -o "$MODELS/ggml-small.bin.part" \
    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin"
  mv "$MODELS/ggml-small.bin.part" "$MODELS/ggml-small.bin"
}

setup_translation() {
  mkdir -p "$MODELS"
  local missing=()
  for entry in "${TRANSLATION_MODELS[@]}"; do
    read -r _ name <<<"$entry"
    [ -f "$MODELS/$name.gguf" ] || missing+=("$entry")
  done
  if [ ${#missing[@]} -eq 0 ]; then
    say "translation models already present"
    return
  fi

  setup_converter
  for entry in "${missing[@]}"; do
    read -r repo name <<<"$entry"
    say "converting $repo"
    "$PYTHON" "$CONVERTER/scripts/convert.py" --model "$repo" --outfile "$MODELS/$name.gguf.part"
    mv "$MODELS/$name.gguf.part" "$MODELS/$name.gguf"
  done
}

setup_converter() {
  CONVERTER="$THIRD_PARTY/sussurro.cpp"
  if [ ! -f "$CONVERTER/scripts/convert.py" ]; then
    say "fetching the sussurro.cpp converter"
    mkdir -p "$THIRD_PARTY"
    git clone --quiet https://github.com/whispem/sussurro.cpp "$CONVERTER"
    git -C "$CONVERTER" checkout --quiet "$SUSSURRO_REV"
  fi
  if [ -n "${AMBIVOX_CONVERT_PYTHON:-}" ]; then
    PYTHON="$AMBIVOX_CONVERT_PYTHON"
    return
  fi
  local venv="$THIRD_PARTY/convert-venv"
  if [ ! -x "$venv/bin/python" ]; then
    say "creating the conversion environment"
    python3 -m venv "$venv"
    "$venv/bin/pip" install --quiet --upgrade pip
    "$venv/bin/pip" install --quiet -r "$CONVERTER/requirements.txt"
  fi
  PYTHON="$venv/bin/python"
}

steps=("$@")
[ ${#steps[@]} -gt 0 ] || steps=(sherpa voices whisper translation)
for step in "${steps[@]}"; do
  case "$step" in
    sherpa|voices|whisper|translation|converter) "setup_$step" ;;
    *) die "unknown step: $step (expected sherpa, voices, whisper, translation or converter)" ;;
  esac
done

say "done: models are in $MODELS"
