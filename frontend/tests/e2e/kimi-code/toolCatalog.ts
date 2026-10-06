import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProcessRow } from '../helpers/processTree'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, readSync, realpathSync, statSync, writeFileSync } from 'node:fs'
import { basename, isAbsolute, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { findBinary } from '../helpers/binaryOnPath'
import { withCleanupSync } from '../helpers/cleanup'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { resolveNativeProcessOwnership } from '../helpers/nativeProcessOwnership'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { processExecutable } from '../helpers/processExecutable'
import { listProcesses } from '../helpers/processTree'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'

export interface KimiCatalogTool {
  name: string
  description: string
  input_schema: null
  source: 'builtin' | 'skill' | 'mcp'
  active: boolean
  mcp_server_id?: string
}

/** The native route lists inactive registry entries and intentionally supplies null schemas. */
export function parseKimiCompleteCatalog(value: unknown): KimiCatalogTool[] {
  if (!isObject(value) || value.code !== 0 || typeof value.msg !== 'string' || typeof value.request_id !== 'string'
    || !value.request_id.trim() || !isObject(value.data) || !Array.isArray(value.data.tools)) {
    throw new Error('The native Kimi catalog reply lacks a successful complete registry envelope.')
  }
  const tools = value.data.tools.map((entry: unknown): KimiCatalogTool => {
    if (!isObject(entry) || typeof entry.name !== 'string' || !entry.name.trim() || typeof entry.description !== 'string'
      || !entry.description.trim() || entry.input_schema !== null || typeof entry.active !== 'boolean'
      || (entry.source !== 'builtin' && entry.source !== 'skill' && entry.source !== 'mcp')
      || (entry.mcp_server_id !== undefined && (typeof entry.mcp_server_id !== 'string' || !entry.mcp_server_id.trim()))) {
      throw new Error('The native Kimi registry contains an incomplete tool descriptor.')
    }
    return {
      name: entry.name,
      description: entry.description,
      input_schema: null,
      source: entry.source,
      active: entry.active,
      ...(typeof entry.mcp_server_id === 'string' ? { mcp_server_id: entry.mcp_server_id } : {}),
    }
  })
  if (!tools.some(tool => tool.source === 'builtin') || new Set(tools.map(tool => tool.name)).size !== tools.length)
    throw new Error('The native Kimi registry has no builtin inventory or repeats a tool identity.')
  return tools
}

/** Parse only the native ready line. A token never enters an error message. */
export function parseKimiCatalogReadyLine(line: string): { origin: string, token: string } | undefined {
  const match = /^Kimi server: (http:\/\/[^/\s#]+)\/?#token=(\S+)\s*$/.exec(line)
  if (!match) {
    if (line.startsWith('Kimi server:'))
      throw new Error('The native Kimi ready line has an invalid shape.')
    return undefined
  }
  const url = new URL(match[1]!)
  if (url.hostname !== '127.0.0.1' || url.protocol !== 'http:' || !url.port || url.port === '0'
    || url.username || url.password || url.search || url.hash) {
    throw new Error('The native Kimi catalog server must use its private loopback origin.')
  }
  return { origin: url.origin, token: match[2]! }
}

export interface KimiCatalogCapture {
  directory: string
  scriptPath: string
  receiptPath: string
  nonce: string
  executable: string
  launchArgs: readonly string[]
  runtimeExecutable: string
  runtimeInvocation: string
  wrapperExecutable: string
  nativeIsScript: boolean
}

export interface KimiCatalogReceipt {
  nonce: string
  nativePid: number
  wrapperPid: number
  executable: string
  argv: string[]
  workingDir: string
  home: string
  origin: string
  token: string
}

export interface KimiNativeHeaderIO {
  open: (path: string) => number
  read: (descriptor: number, buffer: Buffer) => number
  close: (descriptor: number) => void
}

const nativeHeaderIO: KimiNativeHeaderIO = {
  open: path => openSync(path, 'r'),
  read: (descriptor, buffer) => readSync(descriptor, buffer, 0, buffer.length, 0),
  close: closeSync,
}

/** Read only the first native file line and close the same descriptor, including descriptor zero. */
export function readKimiNativeHeader(executable: string, io: KimiNativeHeaderIO = nativeHeaderIO): string {
  const descriptor = io.open(executable)
  return withCleanupSync(() => {
    const header = Buffer.alloc(512)
    return header.subarray(0, io.read(descriptor, header)).toString('utf8').split('\n')[0]!.trim()
  }, () => io.close(descriptor))
}

/** Pass the real native process bytes through unchanged and capture its actual private ready line. */
export function createKimiCatalogCapture(directory: string, launch: NativeStartupLaunch, environment: NodeJS.ProcessEnv = {}): KimiCatalogCapture {
  if (launch.binaryName !== 'kimi' || !launch.executable)
    throw new Error('The Kimi catalog capture requires the actual Kimi executable.')
  mkdirSync(directory, { recursive: true })
  const executable = realpathSync(launch.executable)
  const firstLine = readKimiNativeHeader(executable)
  const envNode = firstLine === '#!/usr/bin/env node'
  const absoluteInterpreter = /^#!(\S+)$/.exec(firstLine)?.[1]
  const absoluteJavaScript = absoluteInterpreter !== undefined && isAbsolute(absoluteInterpreter)
    && ['node', 'node.exe', 'bun', 'bun.exe'].includes(basename(absoluteInterpreter).toLowerCase())
  const nativeIsScript = envNode || absoluteJavaScript
  if (firstLine.startsWith('#!') && !nativeIsScript)
    throw new Error('The native Kimi CLI header uses an unsupported or relative JavaScript interpreter.')
  const interpreter = envNode ? findBinary('node', environment) : absoluteJavaScript ? absoluteInterpreter : executable
  if (!interpreter)
    throw new Error('The Kimi catalog capture cannot resolve the native Node interpreter from its private environment.')
  const capture = {
    directory,
    scriptPath: join(directory, 'kimi-catalog.cjs'),
    receiptPath: join(directory, 'native-ready.json'),
    nonce: randomUUID(),
    executable,
    launchArgs: [...launch.args ?? []],
    runtimeExecutable: realpathSync(interpreter),
    runtimeInvocation: envNode ? 'node' : interpreter,
    wrapperExecutable: realpathSync(process.execPath),
    nativeIsScript,
  }
  const source = `
const { spawn } = require('node:child_process');
const { writeFileSync, renameSync, rmSync } = require('node:fs');
const configuration = ${JSON.stringify(capture)};
const parseReady = ${parseKimiCatalogReadyLine.toString()};
const argv = process.argv.slice(2);
const runtime = argv[0] === 'web';
const child = spawn(configuration.executable, [...configuration.launchArgs, ...argv], { stdio: ['pipe', 'pipe', 'pipe'], env: process.env });
let failed = false;
let pending = '';
let captured = false;
const decoder = new TextDecoder('utf-8', { fatal: true });
function fail(message, cause) {
  if (failed) return;
  failed = true;
  const causes = cause instanceof AggregateError ? cause.errors : cause ? [cause] : [];
  const details = causes.map(error => typeof error?.code === 'string' ? error.code : error?.name ?? 'Error');
  process.stderr.write(message + (details.length ? ' Causes: ' + details.join(', ') + '.' : '') + '\\n');
  process.exitCode = 125;
  child.kill('SIGTERM');
}
function readLine(line) {
  const ready = parseReady(line);
  if (!ready) return;
  if (captured) throw new Error('The native Kimi process repeated its ready line.');
  if (!Number.isSafeInteger(child.pid) || child.pid <= 0) throw new Error('The native Kimi process has no valid PID.');
  const temporary = configuration.receiptPath + '.' + process.pid + '.partial';
  try {
    writeFileSync(temporary, JSON.stringify({ nonce: configuration.nonce, nativePid: child.pid, wrapperPid: process.pid, executable: configuration.executable, argv: [...configuration.launchArgs, ...argv], workingDir: process.cwd(), home: process.env.HOME ?? '', ...ready }), { mode: 0o600, flag: 'wx' });
    renameSync(temporary, configuration.receiptPath);
  }
  catch (cause) {
    try { rmSync(temporary, { force: true }); }
    catch (cleanupCause) { throw new AggregateError([cause, cleanupCause], 'The native Kimi receipt and its cleanup failed.'); }
    throw cause;
  }
  captured = true;
}
function readReady(text, ended = false) {
  pending += text;
  let newline;
  while ((newline = pending.indexOf('\\n')) >= 0) {
    const line = pending.slice(0, newline).replace(/\\r$/, '');
    pending = pending.slice(newline + 1);
    readLine(line);
  }
  if (Buffer.byteLength(pending, 'utf8') > 65536) throw new Error('The native Kimi ready line exceeds its size limit.');
  if (ended && pending) {
    readLine(pending);
    pending = '';
  }
}
process.stdin.pipe(child.stdin);
child.stdin.on('error', () => fail('The Kimi catalog wrapper could not forward native input.'));
child.stdout.on('data', chunk => {
  if (runtime && !failed) {
    try { readReady(decoder.decode(chunk, { stream: true })); }
    catch (cause) { fail('The Kimi catalog wrapper could not capture a valid native ready line.', cause); }
  }
  process.stdout.write(chunk);
});
child.stdout.once('end', () => {
  if (runtime && !failed) {
    try { readReady(decoder.decode(), true); }
    catch (cause) { fail('The Kimi catalog wrapper received incomplete native ready bytes.', cause); }
  }
});
child.stdout.on('error', () => fail('The Kimi catalog wrapper could not read native output.'));
child.stderr.pipe(process.stderr);
child.stderr.on('error', () => fail('The Kimi catalog wrapper could not read native diagnostics.'));
child.once('error', () => {
  failed = true;
  process.stderr.write('The Kimi catalog wrapper could not start the native executable.\\n');
  process.exitCode = 127;
});
child.once('exit', (code, signal) => {
  process.stdin.unpipe(child.stdin);
  process.stdin.pause();
  if (failed) return;
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
  }
  else process.exitCode = code ?? 1;
});
for (const signal of ['SIGTERM', 'SIGINT', 'SIGHUP']) process.on(signal, () => child.kill(signal));
`
  writeFileSync(capture.scriptPath, source, { mode: 0o600 })
  if (process.platform === 'win32') {
    writeFileSync(join(directory, 'kimi.cmd'), `@"${capture.wrapperExecutable}" "${capture.scriptPath}" %*\r\n`, { mode: 0o700 })
  }
  else {
    writeFileSync(join(directory, 'kimi'), `#!/bin/sh\nexec ${quotePosixShellArgument(capture.wrapperExecutable)} ${quotePosixShellArgument(capture.scriptPath)} "$@"\n`, { mode: 0o700 })
  }
  return capture
}

/** Validate an actual ready receipt without exposing its native bearer token. */
export function parseKimiCatalogReceipt(value: unknown, capture: Pick<KimiCatalogCapture, 'nonce' | 'executable' | 'launchArgs'>): KimiCatalogReceipt {
  if (!isObject(value) || value.nonce !== capture.nonce || value.executable !== capture.executable
    || typeof value.nativePid !== 'number' || !Number.isSafeInteger(value.nativePid) || value.nativePid <= 0
    || typeof value.wrapperPid !== 'number' || !Number.isSafeInteger(value.wrapperPid) || value.wrapperPid <= 0 || value.nativePid === value.wrapperPid
    || typeof value.workingDir !== 'string' || !value.workingDir || typeof value.home !== 'string' || !value.home
    || typeof value.origin !== 'string' || typeof value.token !== 'string' || !value.token
    || !Array.isArray(value.argv) || !value.argv.every((arg: unknown) => typeof arg === 'string')) {
    throw new Error('The Kimi native catalog receipt has an invalid process or capture identity.')
  }
  const expected = [...capture.launchArgs, 'web', '--no-open', '--host', '127.0.0.1', '--port', '0', '--log-level', 'warn']
  if (JSON.stringify(value.argv) !== JSON.stringify(expected))
    throw new Error('The Kimi native catalog receipt does not identify the actual Worker server invocation.')
  const ready = parseKimiCatalogReadyLine(`Kimi server: ${value.origin}/#token=${value.token}`)
  if (!ready || ready.origin !== value.origin || ready.token !== value.token)
    throw new Error('The Kimi native catalog receipt has an invalid ready origin.')
  return {
    nonce: capture.nonce,
    nativePid: value.nativePid,
    wrapperPid: value.wrapperPid,
    executable: capture.executable,
    argv: value.argv,
    workingDir: value.workingDir,
    home: value.home,
    origin: ready.origin,
    token: ready.token,
  }
}

export interface KimiCatalogOwner {
  workerExecutable: string
  workerDataDir: string
  capture: Pick<KimiCatalogCapture, 'executable' | 'scriptPath' | 'runtimeExecutable' | 'runtimeInvocation' | 'wrapperExecutable' | 'nativeIsScript' | 'launchArgs'>
}

function exactRuntimeCommand(command: string, executable: string, args: readonly string[], invocation = executable): boolean {
  const prefixes = [executable, basename(executable), invocation].flatMap(value => [value, `"${value}"`, `'${value}'`])
  const forms = [
    args.join(' '),
    args.map(value => /\s/.test(value) ? `"${value.replaceAll('"', '\\"')}"` : value).join(' '),
    args.map(value => /\s/.test(value) ? quotePosixShellArgument(value) : value).join(' '),
  ]
  return prefixes.some(prefix => forms.some(argv => command === `${prefix} ${argv}`))
}

/** Verify physical executables and exact commands at the same owned native child and wrapper PIDs. */
export async function assertKimiCatalogOwnership(
  receipt: KimiCatalogReceipt,
  rows: readonly ProcessRow[],
  owner: KimiCatalogOwner,
  readExecutable: (pid: number) => Promise<string> = processExecutable,
): Promise<void> {
  const { workerExecutable, workerDataDir, capture } = owner
  if (!workerDataDir || new Set(rows.map(row => row.pid)).size !== rows.length)
    throw new Error('The Kimi ownership snapshot lacks an exact private Worker directory or distinct process identities.')
  const native = rows.find(row => row.pid === receipt.nativePid)
  const wrapper = rows.find(row => row.pid === receipt.wrapperPid)
  if (!native || !wrapper || native.ppid !== wrapper.pid)
    throw new Error('The captured Kimi native child does not belong to its actual ready wrapper.')
  if (receipt.executable !== capture.executable)
    throw new Error('The captured Kimi native CLI differs from its actual launch.')
  const nativeArgs = [...(capture.nativeIsScript ? [capture.executable] : []), ...receipt.argv]
  const wrapperArgs = [capture.scriptPath, ...receipt.argv.slice(capture.launchArgs.length)]
  if (!exactRuntimeCommand(wrapper.rawCommand ?? wrapper.command, capture.wrapperExecutable, wrapperArgs))
    throw new Error('The captured Kimi ready wrapper has another exact process command.')
  const [nativeExecutable, wrapperExecutable] = await Promise.all([readExecutable(native.pid), readExecutable(wrapper.pid)])
  if (nativeExecutable !== capture.runtimeExecutable || wrapperExecutable !== capture.wrapperExecutable
    || (native.executable !== undefined && native.executable !== nativeExecutable)
    || (wrapper.executable !== undefined && wrapper.executable !== wrapperExecutable)) {
    throw new Error('The captured Kimi native child or wrapper has another physical executable.')
  }
  const nativeCommand = native.rawCommand ?? native.command
  // The installed CLI sets this exact process title before it prints its native ready line.
  if (nativeCommand !== 'kimi-code' && !exactRuntimeCommand(nativeCommand, capture.runtimeExecutable, nativeArgs, capture.runtimeInvocation))
    throw new Error('The captured Kimi native CLI has another process title or exact command.')
  const ownership = resolveNativeProcessOwnership(rows, native.pid, workerExecutable)
  const worker = rows.find(row => row.pid === ownership.workerPid)
  const command = worker?.rawCommand ?? worker?.command ?? ''
  const exactDirectory = [` --data-dir ${workerDataDir}`, ` --data-dir "${workerDataDir}"`, ` --data-dir '${workerDataDir}'`]
    .some(suffix => command.endsWith(suffix))
  if (!ownership.ownedPids.includes(wrapper.pid) || !worker || !exactDirectory)
    throw new Error('The captured Kimi native server does not belong to the exact private Worker.')
}

/** Use the exact native session query and bearer token on the owned loopback route. */
export async function queryKimiCompleteCatalog(connection: { origin: string, token: string, sessionId: string }): Promise<KimiCatalogTool[]> {
  if (!connection.sessionId.trim() || !parseKimiCatalogReadyLine(`Kimi server: ${connection.origin}/#token=${connection.token}`))
    throw new Error('The Kimi native registry query requires its exact session and ready connection.')
  const url = new URL('/api/v1/tools', connection.origin)
  url.searchParams.set('session_id', connection.sessionId)
  const response = await fetch(url, { headers: { Authorization: `Bearer ${connection.token}` }, signal: AbortSignal.timeout(30_000) })
  if (!response.ok)
    throw new Error(`The owned native Kimi catalog request returned HTTP ${response.status}.`)
  return parseKimiCompleteCatalog(await response.json())
}

/** Query every registered native tool for the current session of the actual owned native server. */
export async function readKimiCompleteCatalog(context: ManagedNativeScenarioContext, capture: KimiCatalogCapture, workerDataDir: string): Promise<KimiCatalogTool[]> {
  const agent = await currentNativeAgent(context)
  if (!agent.agentSessionId || !context.leapmuxServer.agentEnv?.HOME)
    throw new Error('The Kimi complete catalog requires an actual session and private HOME.')
  await expect.poll(() => existsSync(capture.receiptPath)).toBe(true)
  const run = getGlobalState()
  assertPrivateNativePath(capture.directory, run.tmpDir)
  assertPrivateNativePath(capture.receiptPath, run.tmpDir)
  assertPrivateNativePath(workerDataDir, run.tmpDir)
  assertPrivateNativePath(context.leapmuxServer.agentEnv.HOME, run.tmpDir)
  if (process.platform !== 'win32' && (statSync(capture.receiptPath).mode & 0o777) !== 0o600)
    throw new Error('The Kimi native bearer receipt must remain readable only by its owner.')
  const receipt = parseKimiCatalogReceipt(JSON.parse(readFileSync(capture.receiptPath, 'utf8')), capture)
  if (realpathSync(receipt.workingDir) !== realpathSync(agent.workingDir) || realpathSync(receipt.home) !== realpathSync(context.leapmuxServer.agentEnv.HOME))
    throw new Error('The Kimi native server does not use the actual agent directory and private HOME.')
  const owner = { workerExecutable: run.binaryPath, workerDataDir, capture }
  await assertKimiCatalogOwnership(receipt, listProcesses(), owner)
  const catalog = await queryKimiCompleteCatalog({ origin: receipt.origin, token: receipt.token, sessionId: agent.agentSessionId })
  const after = await currentNativeAgent(context)
  if (after.id !== agent.id || after.agentSessionId !== agent.agentSessionId || after.workingDir !== agent.workingDir)
    throw new Error('The Kimi native session changed during its complete registry request.')
  await assertKimiCatalogOwnership(receipt, listProcesses(), owner)
  return catalog
}

/** These native registrations manage files, shell commands, sessions, and domain features. */
export function assertKimiShellCatalog(tools: readonly KimiCatalogTool[]): void {
  const audited = new Set([
    'Bash',
    'Glob',
    'Grep',
    'Read',
    'ReadMediaFile',
    'Write',
    'Edit',
    'Agent',
    'AskUserQuestion',
    'WebSearch',
    'FetchURL',
    'TaskList',
    'TaskOutput',
    'TaskStop',
    'WaitFor',
    'select_tools',
    'Skill',
    'TodoList',
    'EnterPlanMode',
    'ExitPlanMode',
    'CreateGoal',
    'GetGoal',
    'SetGoalBudget',
    'UpdateGoal',
    'CronCreate',
    'CronList',
    'CronDelete',
    'AgentSwarm',
    'NotifyUser',
  ])
  const allowed = (tool: KimiCatalogTool) => (tool.source === 'builtin' && audited.has(tool.name))
    || (tool.source === 'mcp' && tool.name === 'mcp__echo_probe__echo' && tool.mcp_server_id === 'echo_probe')
  if (!tools.some(tool => tool.name === 'Bash' && tool.active) || tools.some(tool => !allowed(tool)))
    throw new Error('The full native Kimi registry contains an unaudited capability or lacks its actual active shell tool.')
}
