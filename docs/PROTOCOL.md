# Protocols

## Server ↔ media engine

A stream of frames over a Unix socket. The server connects; the engine listens.

```
frame   := length:u32 type:u8 stream:u32 payload
length  := 5 + len(payload), at most 65536
```

All integers are big-endian. `stream` is chosen by the server and identifies one
direction of one call (one speaker, one target language). It is `0` for frames
that concern the whole connection.

### Server → engine

| Type | Name | Payload |
|---|---|---|
| `0x01` | OPEN | `source_len:u8 source target_len:u8 target` — language codes |
| `0x02` | AUDIO | `sequence:u16 opus` — one Opus packet and its RTP sequence number |
| `0x03` | CLOSE | empty — finish the stream gracefully |

### Engine → server

| Type | Name | Payload |
|---|---|---|
| `0x00` | HELLO | `version:u16 backend` — sent once, first; current version is `1` |
| `0x81` | SPEECH | `opus` — one 20 ms Opus packet (48 kHz mono) of translated speech |
| `0x82` | CAPTION | `kind:u8 utterance:u32 text` — kind `0` is the transcript, `1` its translation |
| `0x83` | ERROR | `text` — a problem with this stream; the stream stays open unless CLOSED follows |
| `0x84` | CLOSED | empty — the stream is gone and its id may be forgotten |
| `0x85` | STATS | `json` — engine-wide counters, every two seconds |

Text is UTF-8. A frame with an unknown type, a bad length or invalid UTF-8 is a
protocol error and the receiver drops the connection.

### Rules

- The engine answers a refused OPEN (unsupported language, duplicate id) with
  ERROR followed by CLOSED.
- AUDIO for an unknown stream is ignored.
- After CLOSE the engine finishes the utterance in progress, plays out what is
  queued, then sends CLOSED. SPEECH and CAPTION frames may still arrive in
  between.
- SPEECH frames are released in real time, one every 20 ms while there is
  something to say; the server forwards them without buffering.
- Captions for an utterance share its `utterance` number, which counts from zero
  per stream. The transcript always precedes the translation.
- Dropping the connection closes every stream immediately.

`testdata/protocol_vectors.txt` holds one encoded example of each frame. The
Rust and Go test suites both check their codec against it.

## Browser ↔ server

JSON text messages over a WebSocket at `/ws`. The WebSocket must come from the
same origin as the page unless the server was started with `-allow-origin`.

### Client → server

```json
{"type": "join", "room": "kitchen-table", "lang": "fr", "name": "Em"}
{"type": "offer", "sdp": "…"}
{"type": "leave"}
```

`join` must be the first message and arrive within ten seconds. Room names are
1–64 characters from `A-Z a-z 0-9 _ -`. `lang` is one of `en`, `fr`, `es`, `it`.
The offer carries one `sendrecv` audio section with all ICE candidates already
gathered; there is exactly one offer per session.

### Server → client

```json
{"type": "joined", "id": "9f2c…", "room": "kitchen-table", "lang": "fr", "name": "Em",
 "connected": true, "backend": "sussurro", "languages": ["en", "fr", "es", "it"],
 "peer": {"id": "41aa…", "name": "Bob", "lang": "en"},
 "iceServers": [{"urls": ["stun:stun.example.org:3478"]}]}
{"type": "answer", "sdp": "…"}
{"type": "peer-joined", "peer": {"id": "41aa…", "name": "Bob", "lang": "en"}}
{"type": "peer-left", "id": "41aa…"}
{"type": "caption", "from": "9f2c…", "kind": "source", "utterance": 3, "text": "bonjour"}
{"type": "caption", "from": "9f2c…", "kind": "translation", "utterance": 3, "text": "hello"}
{"type": "media", "connected": false, "backend": ""}
{"type": "error", "message": "…"}
```

`peer` and `iceServers` are omitted when there is nothing to report. Captions go
to both participants; `from` is the speaker. `media` announces that the
translation engine went away or came back.

A join that cannot be honoured (full room, room limit, invalid name or language)
gets an `error` and the socket is closed with status 1008. Errors after joining
leave the session open. On shutdown the server closes with status 1001.
