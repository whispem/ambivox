// Opens the web client twice in headless Chrome, feeds both microphones with a
// phrase spoken by the simulated backend, and checks the translated captions.
import { spawn, execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import puppeteer from 'puppeteer-core';

const CHROME_CANDIDATES = [
  process.env.CHROME,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
];
const PORT = process.env.PORT ?? '8097';
const TIMEOUT_MS = 40_000;

const required = (name) => {
  const value = process.env[name];
  if (!value || !existsSync(value)) throw new Error(`${name} must point to a built binary (run via \`make browser-check\`)`);
  return value;
};
const mediaBin = required('MEDIA_BIN');
const serverBin = required('SERVER_BIN');
const chrome = CHROME_CANDIDATES.find((path) => path && existsSync(path));
if (!chrome) throw new Error('Chrome not found; set CHROME to its executable');

const dir = mkdtempSync(join(tmpdir(), 'ambivox-browser-'));
const socket = join(dir, 'media.sock');
const wav = join(dir, 'hello.wav');
execFileSync(mediaBin, ['synth', '--text', 'hello world', '--lang', 'en', '--wav', wav]);

const children = [
  spawn(mediaBin, ['serve', '--socket', socket], { stdio: 'ignore' }),
  spawn(serverBin, ['-listen', `127.0.0.1:${PORT}`, '-media-socket', socket], { stdio: 'ignore' }),
];
let browser;
const cleanup = async () => {
  await browser?.close().catch(() => {});
  children.forEach((child) => child.kill());
  rmSync(dir, { recursive: true, force: true });
};

async function waitForServer() {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    try {
      if ((await fetch(`http://127.0.0.1:${PORT}/readyz`)).ok) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error('the server never became ready');
}

try {
  await waitForServer();
  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: [
      '--no-sandbox',
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
      `--use-file-for-fake-audio-capture=${wav}`,
      '--autoplay-policy=no-user-gesture-required',
    ],
  });

  const open = async (lang, name) => {
    const page = await browser.newPage();
    page.on('pageerror', (error) => console.error(`[${name}] ${error.message}`));
    await page.goto(`http://127.0.0.1:${PORT}/?room=browser-check&lang=${lang}&name=${name}&auto=1&raw=1`);
    return page;
  };
  const alice = await open('en', 'Alice');
  const bob = await open('fr', 'Bob');

  const captionFrom = (page, who) =>
    page
      .waitForFunction(
        (speaker) => {
          const item = [...document.querySelectorAll('#captions li.theirs')].find(
            (li) => li.dataset.translationFinal && li.querySelector('.who').textContent === speaker,
          );
          return item ? { source: item.dataset.source, translation: item.dataset.translation } : false;
        },
        { timeout: TIMEOUT_MS, polling: 200 },
        who,
      )
      .then((handle) => handle.jsonValue());

  const heardByBob = await captionFrom(bob, 'Alice');
  const heardByAlice = await captionFrom(alice, 'Bob');
  for (const page of [alice, bob]) {
    const formShown = await page.evaluate(() => getComputedStyle(document.getElementById('join')).display !== 'none');
    if (formShown) throw new Error('the join form is still visible during the call');
  }
  const expect = (actual, wanted, label) => {
    if (JSON.stringify(actual) !== JSON.stringify(wanted)) {
      throw new Error(`${label}: got ${JSON.stringify(actual)}, want ${JSON.stringify(wanted)}`);
    }
  };
  expect(heardByBob, { source: 'hello world', translation: 'bonjour monde' }, 'Bob hearing Alice');
  expect(heardByAlice, { source: 'hello world', translation: 'hello world' }, 'Alice hearing Bob');
  console.log('browser check passed: captions translated end to end in Chrome');
} catch (error) {
  console.error(`browser check failed: ${error.message}`);
  process.exitCode = 1;
} finally {
  await cleanup();
}
