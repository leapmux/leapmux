import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { mkdirSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { ZCODE_EVENT, ZCODE_METHOD } from '../../../src/generated/contracts/zcode-protocol'
import { writeNodeLauncher } from '../helpers/nodeLauncher'

const RECOVERY_WIRE = {
  send: 'session/send',
  events: 'session/events',
  subscribe: 'session/subscribe',
  event: 'session/event',
  messages: 'session/messages',
  stateUpdated: ZCODE_METHOD.StateUpdated,
  started: ZCODE_EVENT.TurnStarted,
  completed: ZCODE_EVENT.TurnCompleted,
  failed: ZCODE_EVENT.TurnFailed,
  snapshots: [
    'session/read',
    'session/create',
    'session/resume',
    'session/setModel',
    'session/setMode',
    'session/setThoughtLevel',
  ],
}

type RecoveryWire = typeof RECOVERY_WIRE
export type RecoveryDirection = 'worker' | 'native'
interface SelectedTurn { sessionId: string, inputId: string, turnId?: string, omittedSeq?: number }

/** Present one omitted native turn end as a consistent event log. Other payloads retain their native values. */
export function createZcodeRecoveryTransformer(marker: string, wire: RecoveryWire = RECOVERY_WIRE) {
  if (!marker.trim())
    throw new Error('The recovery relay requires a prompt marker.')
  let selected: SelectedTurn | undefined
  const pending = new Map<string, { method: string, sessionId: string }>()
  function object(value: unknown): value is Record<string, unknown> {
    return value !== null && typeof value === 'object' && !Array.isArray(value)
  }
  function string(value: unknown): string {
    return typeof value === 'string'
      ? value
      : ''
  }
  function requestKey(value: unknown): string | undefined {
    return typeof value === 'string' || (typeof value === 'number' && Number.isSafeInteger(value))
      ? JSON.stringify(value)
      : undefined
  }
  function sequence(value: unknown): number {
    if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0)
      throw new Error('The recovery relay received an invalid event sequence.')
    return value
  }
  function visibleSequence(value: unknown): number {
    const current = sequence(value)
    const omitted = selected?.omittedSeq
    return omitted !== undefined && current >= omitted
      ? current - 1
      : current
  }
  function nativeCursor(value: unknown): number {
    const current = sequence(value)
    const omitted = selected?.omittedSeq
    return omitted !== undefined && current >= omitted - 1
      ? current + 1
      : current
  }
  function eventSession(event: Record<string, unknown>, hint: string): string {
    return string(event.sessionId) || hint
  }
  function observeEvent(event: unknown, hint: string): void {
    if (!object(event) || !selected || eventSession(event, hint) !== selected.sessionId)
      return
    const payload = object(event.payload)
      ? event.payload
      : {}
    if (event.type === wire.started && payload.inputId === selected.inputId && !payload.inputSource) {
      const turnId = string(event.turnId)
      if (!turnId)
        throw new Error('The selected native turn start has no turn ID.')
      if (selected.turnId !== undefined && selected.turnId !== turnId)
        throw new Error('The selected native input started more than one turn.')
      selected.turnId = turnId
    }
    if (selected.turnId === undefined || event.turnId !== selected.turnId || (event.type !== wire.completed && event.type !== wire.failed))
      return
    const endSequence = sequence(event.seq)
    if (endSequence === 0)
      throw new Error('The selected native turn end has no positive sequence.')
    if (selected.omittedSeq !== undefined && selected.omittedSeq !== endSequence)
      throw new Error('The selected native turn has more than one end event.')
    selected.omittedSeq = endSequence
  }
  function observeDocument(document: Record<string, unknown>, hint: string): void {
    const session = object(document.session)
      ? document.session
      : {}
    const sessionId = string(document.sessionId) || string(session.sessionId) || hint
    if (Array.isArray(document.events)) {
      // Discover starts first even when a fetched batch arrives out of order.
      for (const event of document.events) {
        if (object(event) && event.type === wire.started)
          observeEvent(event, sessionId)
      }
      for (const event of document.events)
        observeEvent(event, sessionId)
    }
    if (object(document.snapshot))
      observeDocument(document.snapshot, sessionId)
  }
  function projectEvent(event: unknown, hint: string): unknown | null {
    if (!object(event) || !selected || eventSession(event, hint) !== selected.sessionId || selected.omittedSeq === undefined)
      return event
    if (event.turnId === selected.turnId && event.seq === selected.omittedSeq && (event.type === wire.completed || event.type === wire.failed))
      return null
    if (event.seq === undefined)
      return event
    const seq = visibleSequence(event.seq)
    return seq === event.seq
      ? event
      : { ...event, seq }
  }
  function projectDocument(document: Record<string, unknown>, hint: string): Record<string, unknown> {
    const session = object(document.session)
      ? document.session
      : {}
    const sessionId = string(document.sessionId) || string(session.sessionId) || hint
    let result = document
    function replace(key: string, value: unknown): void {
      if (value !== document[key])
        result = { ...result, [key]: value }
    }
    const originalEvents = document.events
    if (Array.isArray(originalEvents)) {
      const events = originalEvents.map(event => projectEvent(event, sessionId)).filter(event => event !== null)
      if (events.length !== originalEvents.length || events.some((event, index) => event !== originalEvents[index]))
        replace('events', events)
    }
    if (selected?.omittedSeq !== undefined && sessionId === selected.sessionId) {
      if (document.eventSeq !== undefined)
        replace('eventSeq', visibleSequence(document.eventSeq))
      if (object(document.runtime) && document.runtime.eventSeq !== undefined) {
        const eventSeq = visibleSequence(document.runtime.eventSeq)
        if (eventSeq !== document.runtime.eventSeq)
          replace('runtime', { ...document.runtime, eventSeq })
      }
    }
    if (object(document.snapshot))
      replace('snapshot', projectDocument(document.snapshot, sessionId))
    return result
  }
  function workerFrame(frame: Record<string, unknown>): Record<string, unknown> {
    const method = string(frame.method)
    const params = object(frame.params)
      ? frame.params
      : {}
    const sessionId = string(params.sessionId)
    if (method === wire.send && typeof params.content === 'string' && params.content.includes(marker)) {
      const inputId = string(params.inputId)
      if (!sessionId || !inputId)
        throw new Error('The selected native prompt has no session or input ID.')
      if (selected && (selected.sessionId !== sessionId || selected.inputId !== inputId))
        throw new Error('The recovery marker matched more than one native input.')
      selected ??= { sessionId, inputId }
    }
    const key = requestKey(frame.id)
    if (method && key !== undefined)
      pending.set(key, { method, sessionId })
    if (selected?.omittedSeq === undefined || sessionId !== selected.sessionId || (method !== wire.events && method !== wire.subscribe) || params.afterSeq === undefined)
      return frame
    const afterSeq = nativeCursor(params.afterSeq)
    return afterSeq === params.afterSeq
      ? frame
      : {
          ...frame,
          params: { ...params, afterSeq },
        }
  }
  function nativeFrame(frame: Record<string, unknown>): Record<string, unknown> | null {
    if (frame.method === wire.stateUpdated)
      return frame
    if (frame.method === wire.event && object(frame.params)) {
      const nested = frame.params.event
      const event = object(nested)
        ? nested
        : frame.params
      const hint = string(frame.params.sessionId) || selected?.sessionId || ''
      observeEvent(event, hint)
      const projected = projectEvent(event, hint)
      if (projected === null)
        return null
      return projected === event
        ? frame
        : {
            ...frame,
            params: object(nested)
              ? { ...frame.params, event: projected }
              : projected,
          }
    }
    // A native request may reuse the numeric ID of an unrelated Worker request.
    if (typeof frame.method === 'string')
      return frame
    const key = requestKey(frame.id)
    const request = key === undefined
      ? undefined
      : pending.get(key)
    if (!request || key === undefined)
      return frame
    pending.delete(key)
    if (!object(frame.result) || request.method === wire.messages)
      return frame
    if (request.method !== wire.events && request.method !== wire.subscribe && !wire.snapshots.includes(request.method))
      return frame
    observeDocument(frame.result, request.sessionId)
    const result = projectDocument(frame.result, request.sessionId)
    return result === frame.result
      ? frame
      : { ...frame, result }
  }
  function transform(direction: RecoveryDirection, line: string): string | null {
    let decoded: unknown
    try {
      decoded = JSON.parse(line)
    }
    catch { return line }
    if (!object(decoded))
      return line
    const projected = direction === 'worker'
      ? workerFrame(decoded)
      : nativeFrame(decoded)
    if (projected === null)
      return null
    if (projected === decoded)
      return line
    const ending = line.endsWith('\r\n')
      ? '\r\n'
      : line.endsWith('\n')
        ? '\n'
        : ''
    return JSON.stringify(projected) + ending
  }
  return {
    transform,
    selected: () => selected === undefined
      ? undefined
      : { ...selected },
  }
}

export interface ZcodeRecoveryRelay { directory: string, scriptPath: string, evidencePath: string }

/** Run the installed native process through the controlled omission. Auxiliary invocations pass through. */
export function writeZcodeRecoveryRelay(directory: string, options: { launch: NativeStartupLaunch, marker: string, evidencePath: string }): ZcodeRecoveryRelay {
  if (![directory, options.launch.executable, options.evidencePath].every(isAbsolute))
    throw new Error('The recovery relay requires absolute paths.')
  createZcodeRecoveryTransformer(options.marker)
  mkdirSync(directory, { recursive: true })
  writeFileSync(options.evidencePath, '', { mode: 0o600 })
  const scriptPath = join(directory, 'recovery-relay.cjs')
  const config = {
    executable: options.launch.executable,
    args: [
      ...options.launch.args ?? [],
    ],
    marker: options.marker,
    evidencePath: options.evidencePath,
    wire: RECOVERY_WIRE,
  }
  const source = `
const { Buffer } = require('node:buffer');
const { spawn } = require('node:child_process');
const { appendFileSync } = require('node:fs');
const config = ${JSON.stringify(config)};
const createTransformer = ${createZcodeRecoveryTransformer.toString()};
const transform = createTransformer(config.marker, config.wire);
const argv = [...config.args, ...process.argv.slice(2)];
const controlled = argv.includes('app-server') && argv.includes('--stdio') && !argv.includes('--prepare-storage');
const child = spawn(config.executable, argv, { stdio: controlled ? ['pipe', 'pipe', 'pipe'] : 'inherit', env: process.env });
let failed = false;
let closing = false;
function fail(error) {
  if (failed || closing) return;
  failed = true;
  process.exitCode = 1;
  process.stderr.write('The ZCode recovery relay failed: ' + String(error) + '\\n');
  process.stdin.destroy();
  child.kill('SIGTERM');
}
function evidence(direction, raw, forwarded, fragment) {
  appendFileSync(config.evidencePath, JSON.stringify({ direction, raw: raw.toString('base64'), forwarded: forwarded === null ? null : forwarded.toString('base64'), fragment }) + '\\n');
}
function write(output, bytes) {
  return new Promise((resolve, reject) => output.write(bytes, error => error ? reject(error) : resolve()));
}
async function forward(direction, input, output) {
  let tail = Buffer.alloc(0);
  try {
    for await (const chunk of input) {
      const bytes = Buffer.concat([tail, chunk]);
      let start = 0;
      for (;;) {
        const end = bytes.indexOf(10, start);
        if (end < 0) break;
        const raw = bytes.subarray(start, end + 1);
        const text = raw.toString('utf8');
        const projected = transform.transform(direction, text);
        const forwarded = projected === null ? null : projected === text ? raw : Buffer.from(projected, 'utf8');
        evidence(direction, raw, forwarded, false);
        if (forwarded !== null) await write(output, forwarded);
        start = end + 1;
      }
      tail = Buffer.from(bytes.subarray(start));
    }
    if (tail.length) { evidence(direction, tail, tail, true); await write(output, tail); }
    if (direction === 'worker') output.end();
  } catch (error) { fail(error); }
}
if (controlled) {
  void forward('worker', process.stdin, child.stdin);
  void forward('native', child.stdout, process.stdout);
  child.stderr.pipe(process.stderr, { end: false });
  child.stdin.on('error', error => { if (child.exitCode === null && child.signalCode === null) fail(error); });
  child.stdout.on('error', fail);
}
child.once('error', fail);
child.once('close', (code, signal) => {
  closing = true;
  if (controlled) process.stdin.destroy();
  if (failed) process.exitCode = 1;
  else if (signal) { process.removeAllListeners(signal); process.kill(process.pid, signal); }
  else process.exitCode = code ?? 1;
});
for (const signal of ['SIGTERM', 'SIGINT', 'SIGHUP']) process.on(signal, () => child.kill(signal));
`
  writeFileSync(scriptPath, source, { mode: 0o600 })
  writeNodeLauncher(directory, options.launch.binaryName, { node: process.execPath, script: scriptPath })
  return { directory, scriptPath, evidencePath: options.evidencePath }
}
