# Operations

## Processes

Run the media engine first, then the server. Either can be restarted on its own:
the server reconnects to the engine with backoff, and calls in progress resume
translating when it returns.

```bash
ambivox-media serve --socket /run/ambivox/media.sock --backend sussurro --models /var/lib/ambivox/models
ambivox -listen :8080 -media-socket /run/ambivox/media.sock
```

`ambivox-media serve` runs a recognition self-test at start-up and exits with a
clear message if the model directory or the whisper model is missing.
Translation models and voices load the first time a language pair needs them.

## Configuration

### ambivox (server)

Every flag has an environment variable; flags win.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `-listen` | `AMBIVOX_LISTEN` | `:8080` | HTTP listen address |
| `-media-socket` | `AMBIVOX_MEDIA_SOCKET` | `/tmp/ambivox-media.sock` | media engine socket |
| `-max-rooms` | `AMBIVOX_MAX_ROOMS` | `100` | simultaneous rooms |
| `-ice-servers` | `AMBIVOX_ICE_SERVERS` | none | STUN/TURN URLs given to both ends |
| `-public-ip` | `AMBIVOX_PUBLIC_IP` | none | address to advertise behind 1:1 NAT |
| `-udp-ports` | `AMBIVOX_UDP_PORTS` | any | UDP range for media, `MIN-MAX` |
| `-ice-loopback` | `AMBIVOX_ICE_LOOPBACK=1` | off | offer loopback candidates |
| `-allow-origin` | `AMBIVOX_ALLOW_ORIGIN` | same origin | extra WebSocket origins |
| `-tls-cert`, `-tls-key` | `AMBIVOX_TLS_CERT`, `AMBIVOX_TLS_KEY` | none | serve HTTPS directly |
| `-log-level` | `AMBIVOX_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `-log-format` | `AMBIVOX_LOG_FORMAT` | `text` | `text` or `json` |

### ambivox-media

| Flag | Default | Meaning |
|---|---|---|
| `--socket` | `/tmp/ambivox-media.sock` | socket to listen on (replaced if it exists) |
| `--backend` | `sim` | `sim` or `sussurro` |
| `--models` | none | model directory, required by `sussurro` |
| `--workers` | `2` | utterances interpreted in parallel |
| `--threads` | `4` | threads each model may use |
| `--bitrate` | `32000` | Opus bitrate of translated speech |
| `--partials-ms` | `800` | how often live captions are refreshed while someone speaks; `0` turns them off |

Budget roughly `workers × threads` cores for inference. With two participants
talking in turn, one worker is busy at a time.

### Model directory

```
models/
  ggml-small.bin                       whisper
  tc-en-fr.gguf  tc-en-es.gguf  tc-en-it.gguf
  tc-fr-en.gguf  tc-it-en.gguf  es-en.gguf
  tc-itc-itc.gguf                      French, Spanish and Italian among themselves
  vits-piper-en_US-ryan-medium/        one Piper voice per language
  vits-piper-fr_FR-tom-medium/
  vits-piper-es_ES-davefx-medium/
  vits-piper-it_IT-paola-medium/
```

`scripts/setup-sussurro.sh` builds this layout. It is the same one sussurro.cpp
uses, plus the whisper model.

## Network

- Browsers only grant microphone access on `https://` or `localhost`. For
  anything beyond one machine, put the server behind a TLS-terminating proxy
  that supports WebSockets, or use `-tls-cert`/`-tls-key`.
- Signalling is HTTP and WebSocket on the listen port. Media is UDP directly
  between each browser and the server; open the range given by `-udp-ports`.
- On a LAN no ICE server is needed. On a host with a public address behind 1:1
  NAT (most cloud instances) set `-public-ip`. Participants behind restrictive
  NATs need a TURN server listed in `-ice-servers`.
- The Unix socket carries unencrypted audio. Keep it on a filesystem only the
  two processes can reach.

## Endpoints

| Path | Purpose |
|---|---|
| `/` | web client |
| `/ws` | signalling WebSocket |
| `/healthz` | liveness: `200` while the process serves HTTP |
| `/readyz` | readiness: `503` while the media engine is unreachable |
| `/config` | languages, backend name and engine link state as JSON |
| `/metrics` | Prometheus text format |

## Metrics

Server:

| Metric | Type | Meaning |
|---|---|---|
| `ambivox_rooms`, `ambivox_peers` | gauge | current load |
| `ambivox_sessions_total` | counter | signalling sessions accepted |
| `ambivox_media_connected` | gauge | engine link up (1) or down (0) |
| `ambivox_media_frames_dropped` | gauge | frames dropped because the engine link was saturated |
| `ambivox_rtp_packets_received_total` | counter | packets from participants |
| `ambivox_rtp_packets_relayed_total` | counter | packets relayed untouched (same language) |
| `ambivox_rtp_packets_translated_total` | counter | packets sent to the engine |
| `ambivox_speech_packets_sent_total` | counter | translated packets sent to participants |
| `ambivox_captions_total` | counter | final captions delivered |
| `ambivox_partial_captions_total` | counter | live caption updates delivered |

Engine counters arrive every two seconds and are re-exported with the prefix
`ambivox_engine_`: `streams`, `packets_in`, `packets_lost`, `packets_dropped`,
`utterances`, `partials`, `partials_skipped`, `frames_out`, `frames_dropped`,
`backlog_dropped_ms`, `errors`, `inference_ms`, `partial_ms`.

Worth alerting on: `ambivox_media_connected == 0`, a rising
`ambivox_engine_errors`, and `ambivox_engine_backlog_dropped_ms` above zero,
which means inference is slower than speech. `inference_ms / utterances` is the
average time to interpret one utterance.

## Troubleshooting

**"Translation engine offline" in the client.** The server cannot reach the
socket. Check that `ambivox-media` is running and that both sides use the same
path.

**Connected, but no audio and no captions.** Media is not arriving. Check the
UDP range and, across NAT, `-public-ip` or a TURN server. `-log-level debug`
shows ICE state changes.

**Captions appear late.** Look at `ambivox_engine_inference_ms` against
`ambivox_engine_utterances`. Raise `--threads`, or use quantised translation
models (see sussurro.cpp).

**Live captions lag or come in bursts.** Each draft re-runs recognition on the
sentence so far. `partial_ms / partials` is the cost of one draft; if it is
close to `--partials-ms`, raise the interval, or set it to `0` on a machine that
struggles: the final captions and the voice are unaffected either way.

**Words are cut or sentences merge.** The voice detector is energy-based; a
headset helps more than any setting.

**Building with CMake 4.** The bundled Opus sources declare an old minimum
CMake version. The Makefile and `.cargo/config.toml` set
`CMAKE_POLICY_VERSION_MINIMUM=3.5` for that reason; export it yourself if you
call CMake-driven builds some other way.
