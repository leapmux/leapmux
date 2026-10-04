import type { ChildProcess } from 'node:child_process'
import { execFile } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, watch, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'vitest'
import { isObject } from '../src/lib/jsonPick'
import { listProcesses } from '../tests/e2e/helpers/processTree'
import { spawnCommandProcess } from './e2eCommandProcess'
import { windowsJobArguments } from './windowsCommandJob'

const WINDOWS_NATIVE_DEADLINE_MS = 45_000
const windowsOnly = describe.runIf(process.platform === 'win32')
const directories = new Set<string>()

const parentSource = String.raw`
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

function write(pathname, value) {
  const draft = pathname + '.writing';
  fs.writeFileSync(draft, JSON.stringify(value));
  fs.renameSync(draft, pathname);
}

function wait(pathname) {
  return new Promise((accept, reject) => {
    const watcher = fs.watch(path.dirname(pathname), check);
    watcher.once('error', error => { watcher.close(); reject(error); });
    function check() {
      if (fs.existsSync(pathname)) { watcher.close(); accept(); }
    }
    check();
  });
}

(async () => {
  const mode = process.argv[2];
  const receipt = process.argv[3];
  if (mode === 'stdio') {
    let input = '';
    process.stdin.setEncoding('utf8');
    process.stdin.on('data', chunk => input += chunk);
    await new Promise(accept => process.stdin.on('end', accept));
    write(receipt, { args: process.argv.slice(4), argv0: process.argv0, input, cwd: process.cwd(), value: process.env.LEAPMUX_JOB_VALUE, empty: process.env.LEAPMUX_JOB_EMPTY, omitted: process.env.LEAPMUX_JOB_OMIT });
    process.stdout.write('stdout:' + input);
    process.stderr.write('stderr:complete');
    return;
  }
  const childReceipt = receipt + '.child';
  const child = spawn(process.execPath, [path.join(path.dirname(receipt), 'child.cjs'), childReceipt], { detached: true, stdio: 'ignore' });
  await wait(childReceipt);
  write(receipt, { rootPid: process.pid, childPid: JSON.parse(fs.readFileSync(childReceipt, 'utf8')).pid });
  child.unref();
  if (mode === 'normal-exit') return;
  await wait(receipt + '.release');
})().catch(error => { console.error(error.message); process.exitCode = 1; });
`

const childSource = String.raw`
const { createServer } = require('node:http');
const fs = require('node:fs');
const receipt = process.argv[2];
createServer((request, response) => response.end('private child')).listen(0, '127.0.0.1', () => {
  const draft = receipt + '.writing';
  fs.writeFileSync(draft, JSON.stringify({ pid: process.pid }));
  fs.renameSync(draft, receipt);
});
`

afterEach(() => {
  for (const directory of directories)
    rmSync(directory, { recursive: true, force: true })
  directories.clear()
})

function fixture() {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'windows-native-job-'))
  directories.add(directory)
  const program = join(directory, 'parent.cjs')
  const receipt = join(directory, 'receipt.json')
  writeFileSync(program, parentSource)
  writeFileSync(join(directory, 'child.cjs'), childSource)
  return { directory, program, receipt }
}

function closed(child: ChildProcess): Promise<{ code: number | null, signal: NodeJS.Signals | null }> {
  return new Promise((accept, reject) => {
    child.once('error', reject)
    child.once('close', (code, signal) => accept({ code, signal }))
  })
}

function receiptReady(path: string, completion: Promise<unknown>): Promise<void> {
  return new Promise((accept, reject) => {
    let finished = false
    const watcher = watch(resolve(path, '..'), check)
    const finish = (error?: unknown) => {
      if (finished)
        return
      finished = true
      watcher.close()
      if (error === undefined)
        accept()
      else
        reject(error)
    }
    function check() {
      if (existsSync(path))
        finish()
    }
    watcher.once('error', finish)
    void completion.then(() => finish(new Error('The native owner exited before the private receipt.')), finish)
    check()
  })
}

function readReceipt(path: string): Record<string, unknown> {
  const value: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!isObject(value))
    throw new Error('The private native receipt is not an object.')
  return value
}

function identities(receipt: Record<string, unknown>) {
  const rows = listProcesses()
  return ['rootPid', 'childPid'].map((field) => {
    const pid = receipt[field]
    if (typeof pid !== 'number' || !Number.isSafeInteger(pid) || pid <= 1)
      throw new Error('The private native receipt has an invalid PID.')
    const row = rows.find(row => row.pid === pid)
    if (!row?.creationTime)
      throw new Error('The private native process has no exact creation identity.')
    return { pid, creationTime: row.creationTime }
  })
}

function requireEnded(captured: { pid: number, creationTime: string }[]): void {
  const rows = listProcesses()
  for (const identity of captured)
    expect(rows.find(row => row.pid === identity.pid)?.creationTime).not.toBe(identity.creationTime)
}

windowsOnly('Windows command job native integration', () => {
  it('preserves quoted arguments, stdin, environment, and the working directory', async () => {
    const data = fixture()
    const args = ['', 'two words', 'a"b', 'trailing slash\\', '日本語', '$literal;&']
    const environment = { ...process.env, LEAPMUX_JOB_VALUE: 'value-保留', LEAPMUX_JOB_EMPTY: '', LEAPMUX_JOB_OMIT: undefined }
    const resource = spawnCommandProcess(process.execPath, [data.program, 'stdio', data.receipt, ...args], { cwd: data.directory, env: environment, stdio: ['pipe', 'pipe', 'pipe'], argv0: 'private argv0' }, { ownTree: true })
    const completion = closed(resource.child)
    let stdout = ''
    let stderr = ''
    resource.child.stdout?.setEncoding('utf8').on('data', chunk => stdout += chunk)
    resource.child.stderr?.setEncoding('utf8').on('data', chunk => stderr += chunk)
    try {
      if (!resource.child.stdin)
        throw new Error('The native owner has no stdin pipe.')
      resource.child.stdin.end('先頭\nfinal42')
      expect(await completion).toEqual({ code: 0, signal: null })
      expect(readReceipt(data.receipt)).toEqual({ args, argv0: 'private argv0', input: '先頭\nfinal42', cwd: data.directory, value: 'value-保留', empty: '' })
      expect(stdout).toBe('stdout:先頭\nfinal42')
      expect(stderr).toBe('stderr:complete')
    }
    finally {
      await resource.stop()
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)

  it('stops a live root and its detached child through their private job', async () => {
    const data = fixture()
    const resource = spawnCommandProcess(process.execPath, [data.program, 'hold', data.receipt], { cwd: data.directory, stdio: 'ignore' }, { ownTree: true })
    const completion = closed(resource.child)
    try {
      await receiptReady(data.receipt, completion)
      const captured = identities(readReceipt(data.receipt))
      await resource.stop()
      await completion
      requireEnded(captured)
    }
    finally {
      writeFileSync(`${data.receipt}.release`, 'release')
      await resource.stop()
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)

  it('stops remaining children after the root exits successfully', async () => {
    const data = fixture()
    const resource = spawnCommandProcess(process.execPath, [data.program, 'normal-exit', data.receipt], { cwd: data.directory, stdio: 'ignore' }, { ownTree: true })
    const completion = closed(resource.child)
    try {
      expect(await completion).toEqual({ code: 0, signal: null })
      const receipt = readReceipt(data.receipt)
      const rows = listProcesses()
      expect(rows.some(row => row.pid === receipt.childPid && row.command.includes('child.cjs'))).toBe(false)
    }
    finally {
      await resource.stop()
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)

  it('cancels an owner during startup without leaving a suspended process', async () => {
    const data = fixture()
    const resource = spawnCommandProcess(process.execPath, [data.program, 'hold', data.receipt], { cwd: data.directory, stdio: 'ignore' }, { ownTree: true })
    const completion = closed(resource.child)
    try {
      await resource.stop()
      await completion
      expect(listProcesses().filter(row => row.command.includes(data.program) || row.command.includes(join(data.directory, 'child.cjs')))).toEqual([])
    }
    finally {
      writeFileSync(`${data.receipt}.release`, 'release')
      await resource.stop()
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)

  it('reports a missing executable without starting a native command', async () => {
    const data = fixture()
    const missing = join(data.directory, 'missing-native-command.exe')
    const resource = spawnCommandProcess(missing, [], { cwd: data.directory, stdio: 'ignore' }, { ownTree: true })
    const completion = closed(resource.child)
    try {
      expect((await completion).code).not.toBe(0)
      expect(listProcesses().filter(row => row.command.includes(missing))).toEqual([])
    }
    finally {
      await resource.stop()
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)

  it('does not wait on or terminate a live PID with a different creation identity', async () => {
    const data = fixture()
    const resource = spawnCommandProcess(process.execPath, [data.program, 'hold', data.receipt], { cwd: data.directory, stdio: 'ignore' }, { ownTree: true })
    const completion = closed(resource.child)
    try {
      await receiptReady(data.receipt, completion)
      const captured = identities(readReceipt(data.receipt))
      const payloadIndex = resource.child.spawnargs.indexOf('-PayloadPath')
      const originalPayloadPath = resource.child.spawnargs[payloadIndex + 1]
      if (payloadIndex < 0 || !originalPayloadPath)
        throw new Error('The native owner has no private payload path.')
      const payload = readReceipt(originalPayloadPath)
      const statePath = join(data.directory, 'different-identity.json')
      const payloadPath = join(data.directory, 'verify-payload.json')
      writeFileSync(statePath, JSON.stringify({ version: 1, ownerPid: resource.child.pid, rootPid: captured[0]!.pid, complete: false, members: [{ pid: captured[0]!.pid, creationTime: '1' }] }))
      writeFileSync(payloadPath, JSON.stringify({ ...payload, statePath }))
      await new Promise<void>((accept, reject) => {
        execFile('powershell.exe', windowsJobArguments('Verify', payloadPath), { windowsHide: true }, (error) => {
          if (error)
            reject(error)
          else
            accept()
        })
      })
      const rows = listProcesses()
      for (const identity of captured)
        expect(rows.find(row => row.pid === identity.pid)?.creationTime).toBe(identity.creationTime)
    }
    finally {
      writeFileSync(`${data.receipt}.release`, 'release')
      await resource.stop()
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)

  it('keeps a second private job alive when the first job stops', async () => {
    const first = fixture()
    const second = fixture()
    const resources = [first, second].map(data => spawnCommandProcess(process.execPath, [data.program, 'hold', data.receipt], { cwd: data.directory, stdio: 'ignore' }, { ownTree: true }))
    const completions = resources.map(resource => closed(resource.child))
    try {
      await Promise.all([receiptReady(first.receipt, completions[0]!), receiptReady(second.receipt, completions[1]!)])
      const firstIds = identities(readReceipt(first.receipt))
      const secondIds = identities(readReceipt(second.receipt))
      await resources[0]!.stop()
      await completions[0]
      requireEnded(firstIds)
      const rows = listProcesses()
      for (const identity of secondIds)
        expect(rows.find(row => row.pid === identity.pid)?.creationTime).toBe(identity.creationTime)
    }
    finally {
      writeFileSync(`${first.receipt}.release`, 'release')
      writeFileSync(`${second.receipt}.release`, 'release')
      await Promise.all(resources.map(resource => resource.stop()))
    }
  }, WINDOWS_NATIVE_DEADLINE_MS)
})
