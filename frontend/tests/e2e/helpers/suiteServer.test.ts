import { chmodSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { refusedHostsReport, startSuiteServer } from './suiteServer'

/**
 * The happy path of `startSuiteServer` is the suite itself: every E2E run
 * starts one and every specification depends on it, so a broken start fails
 * 683 tests at once and names itself in the first of them.
 *
 * What no specification reaches is the FAILURE path, and that path is where a
 * leak hides. `startSuiteServer` binds a model-server port and creates a data
 * directory BEFORE it runs the binary, so a start that fails after those two
 * has to undo them. A leaked port holds a listener for the rest of the process
 * and a leaked directory survives the run.
 */

const roots: string[] = []

afterEach(() => {
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
})

function scratchRoot(): string {
  const root = mkdtempSync(join(tmpdir(), 'leapmux-suite-server-test-'))
  roots.push(root)
  return root
}

describe('startSuiteServer', () => {
  it('rejects when the binary does not exist', async () => {
    const root = scratchRoot()
    await expect(startSuiteServer({ binaryPath: join(root, 'no-such-leapmux'), tmpDir: root }))
      .rejects
      .toThrow(/ENOENT|no-such-leapmux/)
  })

  it('removes the data directory it created when the start fails', async () => {
    // `mkdtempSync` runs before the binary does, so a start that throws after
    // it leaves a directory nothing will ever clean up. The suite creates one
    // per run, and a developer who iterates on a broken build collects them.
    const root = scratchRoot()
    await startSuiteServer({ binaryPath: join(root, 'no-such-leapmux'), tmpDir: root }).catch(() => {})
    expect(readdirSync(root).filter(entry => entry.startsWith('leapmux-e2e-dev-'))).toEqual([])
  })

  it('closes the model server it bound when the start fails', async () => {
    // The mock endpoint binds a port before the binary runs. A start that threw
    // without closing it leaves a listener for the rest of the process, and the
    // next start binds another beside it.
    const root = scratchRoot()
    const before = openServerCount()
    await startSuiteServer({ binaryPath: join(root, 'no-such-leapmux'), tmpDir: root }).catch(() => {})
    expect(await settledServerCount(before)).toBe(before)
  })

  it.skipIf(process.platform === 'win32')('binds the hub\'s local IPC socket at a path that fits sun_path', async () => {
    // macOS refuses a Unix socket path longer than 104 bytes with
    // `bind: invalid argument`, and the run directory of a checkout nested
    // under `.tmp/wt/` is already past it. The default socket is
    // `<data-dir>/hub/hub.sock`, which is as long as the data dir is deep, so
    // the start must pass `LEAPMUX_HUB_LOCAL_LISTEN` at a short path of its
    // own. Dropping that variable fails every spec on a deep checkout with a
    // bind error and names none of them.
    const root = scratchRoot()
    const binary = join(root, 'record-env')
    const recorded = join(root, 'recorded-env.json')
    writeFileSync(binary, `#!/bin/sh\nif [ "$1" = "dev" ]; then\n  node -e 'require("fs").writeFileSync(process.argv[1], JSON.stringify({ listen: process.env.LEAPMUX_HUB_LOCAL_LISTEN, argv: process.argv.slice(2) }))' ${JSON.stringify(recorded)} "$@"\n  exit 1\nfi\nexit 0\n`)
    chmodSync(binary, 0o755)

    await startSuiteServer({ binaryPath: binary, tmpDir: root }).catch(() => {})
    const record = JSON.parse(readFileSync(recorded, 'utf8')) as { listen: string, argv: string[] }

    expect(record.listen).toMatch(/^unix:/)
    const socketPath = record.listen.slice('unix:'.length)
    // The whole point: short enough for the 104-byte sun_path limit, with room
    // to spare so a longer tmpdir prefix still fits.
    expect(socketPath.length, `socket path is ${socketPath.length} bytes: ${socketPath}`).toBeLessThan(104)
    expect(socketPath.endsWith('/hub.sock')).toBe(true)
    // The default would be the deep data dir; a short path must not be it.
    const dataDirIndex = record.argv.indexOf('-data-dir')
    expect(dataDirIndex).toBeGreaterThanOrEqual(0)
    expect(socketPath.startsWith(record.argv[dataDirIndex + 1] ?? '')).toBe(false)
  })
})

/**
 * How many TCP servers this process holds open.
 *
 * `process._getActiveHandles` is undocumented. The probe below is
 * what stops that from making the assertion vacuous: a runtime that does not
 * supply the function would otherwise answer the same number every time and
 * pass whatever happened.
 */
function openServerCount(): number {
  const active = (process as unknown as { _getActiveHandles?: () => unknown[] })._getActiveHandles
  if (typeof active !== 'function')
    return -1
  return active.call(process).filter(handle => handle?.constructor?.name === 'Server').length
}

/**
 * The count once it reaches `expected`, or the last count before the deadline.
 *
 * A closed server keeps its handle for a tick or two after its close callback
 * runs, so a count read immediately after the callback still holds it. Polling
 * removes that window without hiding a real leak: a count that never reaches
 * `expected` is returned as it stands, and the assertion fails with the true
 * number rather than with a timeout.
 */
async function settledServerCount(expected: number, timeoutMs = 2000): Promise<number> {
  const deadline = Date.now() + timeoutMs
  let count = openServerCount()
  while (count !== expected && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 10))
    count = openServerCount()
  }
  return count
}

describe('openServerCount', () => {
  it('sees a server open and close, so the leak assertion is not vacuous', async () => {
    const before = openServerCount()
    expect(before, 'process._getActiveHandles is unavailable').toBeGreaterThanOrEqual(0)
    const server = createServer()
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
    expect(openServerCount()).toBe(before + 1)
    await new Promise<void>(resolve => server.close(() => resolve()))
    expect(await settledServerCount(before)).toBe(before)
  })
})

describe('refusedHostsReport', () => {
  it('states each refused host once, in a stable order, with its count', () => {
    expect(refusedHostsReport(new Map([['github.com:443', 2], ['app.kiro.dev:443', 1]]))).toEqual([
      'The mock proxy refused 1 request to app.kiro.dev:443.',
      'The mock proxy refused 2 requests to github.com:443.',
    ])
  })

  it('states nothing for a run that refused nothing', () => {
    expect(refusedHostsReport(new Map())).toEqual([])
  })
})
