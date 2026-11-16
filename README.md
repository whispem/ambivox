# ambivox

Voice calls where each person speaks their own language and hears the other in
theirs — self-hosted, with no cloud service in the loop.

Two people open a web page, pick a language and talk. ambivox listens to each
speaker, transcribes, translates, re-synthesises the sentence in the listener's
language and plays it over WebRTC, with live captions on both sides. Speech
recognition, translation and synthesis all run on your own machine.

Supported languages: English, French, Spanish and Italian, in every direction.

```
 browser A ──WebRTC──┐                              ┌──WebRTC── browser B
   (fr)              │                              │             (en)
              ┌──────▼──────────────────────────────▼──────┐
              │  ambivox            Go                     │
              │  signalling · rooms · ICE/DTLS/SRTP        │
              └──────────────────┬─────────────────────────┘
                                 │ Unix socket, Opus frames
              ┌──────────────────▼─────────────────────────┐
              │  ambivox-media      Rust                   │
              │  jitter/loss handling · resampling · VAD   │
              │  scheduling · pacing        ┌────────────┐ │
              │                             │ x86-64 and │ │
              │                             │ AArch64    │ │
              │                             │ assembly   │ │
              │                             └────────────┘ │
              │  ┌──────────────────────────────────────┐  │
              │  │ engine              C++              │  │
              │  │ whisper.cpp → sussurro.cpp → Piper   │  │
              │  └──────────────────────────────────────┘  │
              └────────────────────────────────────────────┘
```

## Why four languages

Each one sits on a real boundary:

| Language | Where | Why |
|---|---|---|
| Go | `server/` | WebRTC transport and signalling, built on [Pion](https://github.com/pion/webrtc). |
| Rust | `crates/` | The real-time audio path: no garbage collector, explicit threads, bounded queues. |
| Assembly | `crates/dsp/src/kernels/` | The inner loops of resampling, level metering and sample conversion (SSE2, AVX2+FMA, NEON), each checked against a scalar reference. |
| C++ | `engine/` | Inference. It wraps whisper.cpp, [sussurro.cpp](https://github.com/whispem/sussurro.cpp) and sherpa-onnx behind a small C ABI. |

## Quick start

Requirements: Rust 1.88+, Go 1.24+, CMake 3.16+ (3.28+ for the real backend) and a
C++17 compiler.

```bash
make check    # format, lint, unit tests, and a full call end to end
make run      # start the media engine and the server on http://127.0.0.1:8080
```

`make run` uses the **simulated backend**, which needs no model: it "speaks"
text as a sequence of tones and "recognises" those tones again, with a small
built-in dictionary standing in for translation. It exists so that the whole
pipeline — WebRTC, codec, resampling, voice detection, scheduling — can be
tested deterministically. It does not understand human speech.

## Real speech

The real backend uses whisper.cpp for recognition, sussurro.cpp (OPUS-MT) for
translation and Piper voices through sherpa-onnx for synthesis.

```bash
scripts/setup-sussurro.sh              # downloads the runtime and several GB of models
make run BACKEND=sussurro MODELS=models
```

Open `http://127.0.0.1:8080` in two browser tabs (or on two machines, see
[docs/OPERATIONS.md](docs/OPERATIONS.md)), choose the same room and two
different languages, and talk.

If you already have a sussurro.cpp checkout with its models, point ambivox at
it instead of downloading again:

```bash
ln -s /path/to/sussurro.cpp/third_party/whisper.cpp/models/ggml-small.bin \
      /path/to/sussurro.cpp/models/
export AMBIVOX_SHERPA_DIR=/path/to/sussurro.cpp/third_party/sherpa-onnx
make run BACKEND=sussurro MODELS=/path/to/sussurro.cpp/models
```

## Testing

| Command | What it covers |
|---|---|
| `make test-engine` | C++ engine: tone codec, simulated backend, C ABI error handling. |
| `make test-rust` | Assembly kernels against the scalar reference (with and without the `asm` feature), resampler, voice detection, Opus wrapper, wire protocol, pipeline ordering, loss handling, pacing. |
| `make test-go` | Wire protocol against the same vectors as Rust, engine link and reconnection, rooms, signalling, HTTP endpoints, with real WebRTC connections and the race detector on. |
| `make e2e` | Two WebRTC participants, the Go server and the real `ambivox-media` binary: a spoken phrase goes in, its translation comes out as audio and captions. |
| `make browser-check` | The same call from two headless Chrome tabs running the actual web client (needs Chrome and Node.js; not part of `make check`). |

Continuous integration runs `make check` on Linux x86-64 and macOS arm64, so
both assembly back ends are exercised. The model-backed path is compiled but
not run in CI because of the size of the models.

## Layout

```
engine/            C++ inference engine and its C ABI
crates/dsp/        Rust: SIMD kernels, polyphase resampler, voice activity segmenter
crates/engine/     Rust: safe bindings to the C++ engine
crates/media/      Rust: ambivox-media, the real-time pipeline and its socket server
server/            Go: ambivox, the WebRTC server, plus the embedded web client
testdata/          Protocol vectors shared by the Rust and Go test suites
docs/              Architecture, wire protocol, operations
scripts/           Model setup and the headless browser check
```

## Limits worth knowing

- A room holds two participants.
- Translation is per utterance: the listener hears a sentence once the speaker
  pauses, typically one to three seconds later depending on hardware.
- When both participants pick the same language the audio is relayed untouched.
- The assembly kernels cover x86-64 and AArch64; other targets use the scalar
  Rust implementation (`--no-default-features` forces it anywhere).

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — how audio flows and why it is built this way
- [docs/PROTOCOL.md](docs/PROTOCOL.md) — the server ↔ media engine wire format and the signalling messages
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — configuration, deployment, metrics, troubleshooting

## License

MIT, see [LICENSE](LICENSE). Models and third-party runtimes keep their own
licences: whisper.cpp and ggml (MIT), OPUS-MT models (CC-BY 4.0), SentencePiece
and sherpa-onnx (Apache-2.0), Piper voices (see each model card), Opus (BSD),
Pion (MIT).
