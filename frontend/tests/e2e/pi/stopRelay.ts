import { Buffer } from 'node:buffer'
import { randomBytes } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { PI_DIALOG_METHOD, PI_EVENT } from '../../../src/generated/contracts/pi-protocol'
import { writeNodeLauncher } from '../helpers/nodeLauncher'

export interface PiStopRelayOptions {
  executable: string
  args?: readonly string[]
  originalMarker: string
  replacement?: { prompt: string, title: string }
  retrySignal?: { port: number, nonce: string }
  evidencePath: string
}

/** Hold one actual abort reply until Pi emits the replacement dialog. Preserve every native output byte. */
export function createPiStopRelay(directory: string, options: PiStopRelayOptions): string {
  if (!isAbsolute(options.executable) || !options.originalMarker.trim() || !isAbsolute(options.evidencePath)
    || (options.replacement && (!options.replacement.prompt.trim() || !options.replacement.title.trim()))) {
    throw new Error('The Pi stop relay requires its executable and complete scenario values.')
  }
  if (options.retrySignal && (!Number.isInteger(options.retrySignal.port) || options.retrySignal.port < 1
    || options.retrySignal.port > 65535 || !/^[a-f0-9]{64}$/.test(options.retrySignal.nonce))) {
    throw new Error('The Pi retry signal requires a valid port and private nonce.')
  }
  mkdirSync(directory, { recursive: true })
  const program = join(directory, 'stop-relay.cjs')
  writeFileSync(program, piStopRelayProgram(options))
  return writeNodeLauncher(directory, 'pi', { node: process.execPath, script: program })
}

/** The relay injects native input commands. It forwards only bytes that the native process emitted. */
export function piStopRelayProgram(options: PiStopRelayOptions): string {
  return `
const { Buffer } = require('node:buffer');
const { connect } = require('node:net');
const { spawn } = require('node:child_process');
const { appendFileSync } = require('node:fs');
const options = ${JSON.stringify(options)};
const events = ${JSON.stringify(PI_EVENT)};
const methods = ${JSON.stringify(PI_DIALOG_METHOD)};
const child = spawn(options.executable, [...options.args ?? [], ...process.argv.slice(2)], { stdio: ['pipe', 'pipe', 'pipe'], env: process.env });
let inputTail = Buffer.alloc(0);
let outputTail = Buffer.alloc(0);
let originalSeen = false;
let abortId;
let heldReply;
let replaced = false;
let retrySignalled = false;
const signal = options.retrySignal ? connect({ host: '127.0.0.1', port: options.retrySignal.port }) : undefined;
signal?.on('error', error => { evidence('signal-error', Buffer.from(String(error))); });
const evidence = (kind, raw) => appendFileSync(options.evidencePath, JSON.stringify({ kind, bytes: raw.toString('base64') }) + '\\n');
child.once('spawn', () => evidence('native-process', Buffer.from(JSON.stringify({ pid: child.pid, argv: [...options.args ?? [], ...process.argv.slice(2)] }))));
const decode = raw => {
  try { const value = JSON.parse(raw.toString('utf8')); return value && typeof value === 'object' && !Array.isArray(value) ? value : undefined; }
  catch { return undefined; }
};
const inputLine = raw => {
  const frame = decode(raw);
  if (!frame) return;
  if (frame.type === 'prompt' && typeof frame.message === 'string' && frame.message.includes(options.originalMarker)) originalSeen = true;
  if (originalSeen && abortId === undefined && frame.type === 'abort' && typeof frame.id === 'string' && frame.id !== '') {
    abortId = frame.id;
    evidence('abort-input', raw);
  }
};
const outputLine = raw => {
  const frame = decode(raw);
  evidence('native-output', raw);
  if (!retrySignalled && signal && frame && frame.type === events.AutoRetryStart && typeof frame.errorMessage === 'string' && frame.errorMessage.includes(options.originalMarker)) {
    retrySignalled = true;
    signal.write(JSON.stringify({ nonce: options.retrySignal.nonce, bytes: raw.toString('base64') }) + '\\n');
  }
  if (options.replacement && !replaced && abortId !== undefined && !heldReply && frame && frame.type === events.Response && frame.id === abortId && frame.command === 'abort' && frame.success === true) {
    heldReply = Buffer.from(raw);
    evidence('held-abort-reply', raw);
    const prompt = Buffer.from(JSON.stringify({ id: 'e2e-replacement-' + abortId, type: 'prompt', message: options.replacement.prompt }) + '\\n');
    evidence('replacement-input', prompt);
    child.stdin.write(prompt);
    return;
  }
  process.stdout.write(raw);
  if (heldReply && frame && frame.type === events.ExtensionUIRequest && frame.method === methods.Editor && frame.title === options.replacement.title) {
    evidence('replacement-dialog', raw);
    const reply = heldReply;
    heldReply = undefined;
    replaced = true;
    evidence('released-abort-reply', reply);
    process.stdout.write(reply);
  }
};
const consume = (tail, chunk, line) => {
  const buffer = Buffer.concat([tail, chunk]);
  let start = 0;
  for (;;) {
    const end = buffer.indexOf(10, start);
    if (end < 0) return Buffer.from(buffer.subarray(start));
    line(buffer.subarray(start, end + 1));
    start = end + 1;
  }
};
process.stdin.on('data', chunk => { inputTail = consume(inputTail, chunk, inputLine); child.stdin.write(chunk); });
process.stdin.on('end', () => child.stdin.end());
child.stdin.on('error', error => { evidence('stdin-error', Buffer.from(String(error))); });
child.stdout.on('data', chunk => { outputTail = consume(outputTail, chunk, outputLine); });
child.stdout.on('end', () => {
  if (outputTail.length) process.stdout.write(outputTail);
  if (heldReply) { evidence('unreleased-abort-reply', heldReply); process.stdout.write(heldReply); heldReply = undefined; }
});
child.stderr.pipe(process.stderr, { end: false });
child.on('error', error => { console.error(error); process.exitCode = 1; process.stdin.destroy(); });
child.on('close', code => { process.exitCode = code === null || code < 0 ? 1 : code; process.stdin.destroy(); signal?.end(); });
for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(signal, () => child.kill(signal));
`
}

/** Start one private signal reader. Its handler receives actual retry-start bytes from the relay. */
export async function withPiRetrySignal<T>(
  onStart: (bytes: Buffer) => void,
  use: (signal: { port: number, nonce: string }) => Promise<T>,
): Promise<T> {
  const nonce = randomBytes(32).toString('hex')
  const sockets = new Set<import('node:net').Socket>()
  let failure: Error | undefined
  const server = createServer((socket) => {
    sockets.add(socket)
    socket.once('close', () => sockets.delete(socket))
    socket.on('error', (error) => {
      failure = error
    })
    let tail = ''
    socket.setEncoding('utf8')
    socket.on('data', (chunk: string) => {
      tail += chunk
      for (;;) {
        const end = tail.indexOf('\n')
        if (end < 0)
          return
        const line = tail.slice(0, end)
        tail = tail.slice(end + 1)
        try {
          const packet = JSON.parse(line) as { nonce?: unknown, bytes?: unknown }
          if (!packet || packet.nonce !== nonce || typeof packet.bytes !== 'string'
            || !/^(?:[A-Z0-9+/]{4})*(?:[A-Z0-9+/]{2}==|[A-Z0-9+/]{3}=)?$/i.test(packet.bytes) || packet.bytes === '') {
            throw new Error('The Pi relay signal has an invalid identity or payload.')
          }
          onStart(Buffer.from(packet.bytes, 'base64'))
        }
        catch (error) {
          failure = error instanceof Error ? error : new Error(String(error))
          socket.destroy()
        }
      }
    })
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  if (!address || typeof address === 'string')
    throw new Error('The Pi retry signal requires a loopback TCP address.')
  try {
    const result = await use({ port: address.port, nonce })
    if (failure)
      throw failure
    return result
  }
  finally {
    for (const socket of sockets)
      socket.destroy()
    await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  }
}
