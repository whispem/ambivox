# Changelog

## 0.4.0

- Ready-made images. Every release publishes `ghcr.io/whispem/ambivox` for
  x86-64 and arm64, so `docker compose up` downloads ambivox instead of
  compiling it. The image runs the real speech backend by default and also
  carries the simulated one (`AMBIVOX_BACKEND=sim`).
- `docker compose run --rm models` downloads and prepares the speech models in
  a volume, with nothing to install on the machine.
- The images carry the licences of everything compiled into them.
- `compose.build.yaml` builds the images from the sources;
  `compose.sussurro.yaml` is gone.
- `scripts/setup-sussurro.sh` gains a `converter` step and can use an existing
  Python (`AMBIVOX_CONVERT_PYTHON`).

## 0.3.0

- Runs on a local network with no Internet. `docker compose up` starts the
  media engine and the server; `make lan` does the same without Docker.
- `-tls-self-signed` serves HTTPS with a certificate generated for the
  machine's addresses, kept in `-data-dir` and renewed when they change. The
  server prints the addresses to open and the certificate fingerprint.
- Rooms by invitation. Starting a call creates a random room and a signed link
  that expires after 24 hours (`-invite-ttl`); joining without it is refused.
  `-open-rooms` restores rooms joinable by name.
- `AMBIVOX_ACCESS_KEY` restricts who can start a call.
- `-udp-port` carries every call over a single UDP port.
- Echo guard: while a speaker's translation plays to the listener, the
  speaker's microphone is silenced, which stops two devices in the same room
  from translating each other's loudspeaker in a loop (`-echo-guard`, on by
  default). New metric `ambivox_rtp_packets_muted_total`.
- New endpoint `POST /rooms`; `join` carries a `token`; `/config` reports
  `inviteOnly` and `accessKey`.
- `make offline` runs complete calls in a network namespace with only
  loopback, in CI on Linux.
- The web client starts calls and shares invitation links instead of asking for
  a room name.
- `scripts/setup-sussurro.sh` supports Linux on arm64.
- Documentation: local network setup, privacy, running behind your own reverse
  proxy.

## 0.2.0

- Live captions. While someone speaks, both participants see the sentence being
  transcribed and translated, then the final version replaces the draft. Drafts
  are refreshed every 800 ms (`ambivox-media serve --partials-ms`, `0` turns
  them off) and never delay the translated voice.
- The engine protocol moves to version 2: the CAPTION kind gains a partial bit.
  Upgrade `ambivox` and `ambivox-media` together.
- Captions sent to the browser carry a `final` field.
- New metrics: `ambivox_partial_captions_total`, and the engine counters
  `partials`, `partials_skipped` and `partial_ms`.
- The simulated backend speaks word by word, so live captions can be tested
  without models.
- Fixed: the join form stayed visible during a call.
- The Rust crates move to the 2024 edition.

## 0.1.0

- First release: translated two-person voice calls over WebRTC, with a Go
  server, a Rust media engine with x86-64 and AArch64 assembly kernels, and a
  C++ inference engine backed by whisper.cpp, sussurro.cpp and Piper.
