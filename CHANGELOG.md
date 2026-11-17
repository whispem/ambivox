# Changelog

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
