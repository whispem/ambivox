const $ = (id) => document.getElementById(id);
const params = new URLSearchParams(location.search);
const GATHERING_TIMEOUT_MS = 3000;

const state = { ws: null, pc: null, mic: null, me: null, peer: null, leaving: false };

function setState(name) {
  document.body.dataset.state = name;
}

function showError(text) {
  $('error').textContent = text;
  $('error').hidden = !text;
}

function setStatus(text) {
  $('status').textContent = text;
}

function setPeer(peer) {
  state.peer = peer;
  $('peer-label').textContent = peer
    ? `Talking with ${peer.name} (${peer.lang})`
    : 'Waiting for someone to join…';
}

function send(message) {
  if (state.ws?.readyState === WebSocket.OPEN) state.ws.send(JSON.stringify(message));
}

function addCaption({ from, kind, utterance, text, final = true }) {
  const id = `caption-${from}-${utterance}`;
  let item = document.getElementById(id);
  if (!item) {
    const mine = from === state.me.id;
    item = document.createElement('li');
    item.id = id;
    item.className = mine ? 'mine' : 'theirs';
    for (const part of ['who', 'said', 'heard']) {
      const line = document.createElement(part === 'who' ? 'div' : 'p');
      line.className = part;
      item.append(line);
    }
    item.querySelector('.who').textContent = mine ? 'You' : state.peer?.name ?? 'Guest';
    $('captions').append(item);
  }
  const settled = `${kind}Final`;
  if (!final && item.dataset[settled]) return;
  const mine = item.classList.contains('mine');
  const primary = mine ? 'source' : 'translation';
  item.querySelector(kind === primary ? '.said' : '.heard').textContent = text;
  item.dataset[kind] = text;
  if (final) item.dataset[settled] = '1';
  item.classList.toggle('draft', !(item.dataset.sourceFinal && item.dataset.translationFinal));
  item.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
}

function gatheringComplete(pc) {
  if (pc.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, GATHERING_TIMEOUT_MS);
    pc.addEventListener('icegatheringstatechange', () => {
      if (pc.iceGatheringState === 'complete') {
        clearTimeout(timer);
        resolve();
      }
    });
  });
}

async function connect(iceServers) {
  const pc = new RTCPeerConnection({ iceServers });
  state.pc = pc;
  for (const track of state.mic.getTracks()) pc.addTrack(track, state.mic);
  pc.addEventListener('track', (event) => {
    $('remote').srcObject = event.streams[0] ?? new MediaStream([event.track]);
  });
  pc.addEventListener('connectionstatechange', () => {
    setState(pc.connectionState === 'connected' ? 'connected' : 'connecting');
    if (pc.connectionState === 'failed') showError('The media connection failed.');
  });
  await pc.setLocalDescription(await pc.createOffer());
  await gatheringComplete(pc);
  send({ type: 'offer', sdp: pc.localDescription.sdp });
}

async function handle(message) {
  switch (message.type) {
    case 'joined':
      state.me = message;
      setPeer(message.peer ?? null);
      setStatus(message.connected ? `Translation engine: ${message.backend}` : 'Translation engine offline');
      await connect(message.iceServers ?? []);
      break;
    case 'answer':
      await state.pc.setRemoteDescription({ type: 'answer', sdp: message.sdp });
      break;
    case 'peer-joined':
      setPeer(message.peer);
      break;
    case 'peer-left':
      setPeer(null);
      break;
    case 'caption':
      addCaption(message);
      break;
    case 'media':
      setStatus(message.connected ? `Translation engine: ${message.backend}` : 'Translation engine offline');
      break;
    case 'error':
      showError(message.message);
      break;
  }
}

function hangUp() {
  state.leaving = true;
  send({ type: 'leave' });
  state.ws?.close();
  state.pc?.close();
  state.mic?.getTracks().forEach((track) => track.stop());
  Object.assign(state, { ws: null, pc: null, mic: null, me: null, peer: null });
  $('captions').replaceChildren();
  $('call').hidden = true;
  $('join').hidden = false;
  setState('idle');
}

async function join(room, lang, name) {
  showError('');
  state.leaving = false;
  setState('connecting');
  const processing = params.get('raw') !== '1';
  try {
    state.mic = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: processing, noiseSuppression: processing, autoGainControl: processing },
    });
  } catch (error) {
    setState('idle');
    showError(`Microphone unavailable: ${error.message}`);
    return;
  }

  $('join').hidden = true;
  $('call').hidden = false;
  $('room-label').textContent = room;
  setPeer(null);

  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${scheme}://${location.host}/ws`);
  state.ws = ws;
  ws.addEventListener('open', () => send({ type: 'join', room, lang, name }));
  ws.addEventListener('message', (event) => {
    handle(JSON.parse(event.data)).catch((error) => showError(error.message));
  });
  ws.addEventListener('close', (event) => {
    if (state.leaving || state.ws !== ws) return;
    const reason = event.reason || 'The connection to the server was lost.';
    hangUp();
    showError(reason);
  });
}

$('join').addEventListener('submit', (event) => {
  event.preventDefault();
  join($('room').value.trim(), $('lang').value, $('name').value.trim());
});

$('leave').addEventListener('click', hangUp);

$('mute').addEventListener('click', () => {
  const muted = $('mute').getAttribute('aria-pressed') !== 'true';
  state.mic?.getAudioTracks().forEach((track) => { track.enabled = !muted; });
  $('mute').setAttribute('aria-pressed', String(muted));
  $('mute').textContent = muted ? 'Unmute' : 'Mute';
});

for (const field of ['name', 'room', 'lang']) {
  if (params.has(field)) $(field).value = params.get(field);
}
if (params.get('auto') === '1' && $('room').value) $('join').requestSubmit();
