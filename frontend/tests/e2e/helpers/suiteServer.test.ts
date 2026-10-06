import { chmodSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { Server } from 'node:net'
import { join, resolve } from 'node:path'
import { setImmediate } from 'node:timers/promises'
import { afterEach, describe, expect, it } from 'vitest'
import { withCleanup } from './cleanup'
import { ancestorInstructionsReport, refusedHostsReport, startSuiteServer } from './suiteServer'

/**
 * Every E2E run tests successful suite startup.
 * These unit cases test failures after the mock listener and data directory exist.
 * Failed startup must release both resources.
 */

const roots: string[] = []

afterEach(() => {
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
})

function scratchRoot(): string {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'suite-server-test-'))
  roots.push(root)
  return root
}

// `startSuiteServer` awaits `createMockAgentEnvironment` before it runs the (fake) hub,
// and that call runs the real setup commands of the installed agent CLIs. Each run starts
// operating-system processes, so a case takes 1 to 7 seconds, and vitest's default limit
// of 5 seconds fails a correct run on a busy machine. The limit is generous because no
// case waits on it: a case ends as soon as its start settles.
const START_SUITE_SERVER_TEST_TIMEOUT_MS = 60_000

describe('startSuiteServer', { timeout: START_SUITE_SERVER_TEST_TIMEOUT_MS }, () => {
  it('rejects when the binary does not exist', async () => {
    const root = scratchRoot()
    await expect(startSuiteServer({ binaryPath: join(root, 'no-such-leapmux'), tmpDir: root }))
      .rejects
      .toThrow(/ENOENT|no-such-leapmux/)
  })

  it('removes the data directory it created when the start fails', async () => {
    // Startup creates this directory before it executes the binary. Failure must remove it.
    const root = scratchRoot()
    await startSuiteServer({ binaryPath: join(root, 'no-such-leapmux'), tmpDir: root }).catch(() => {})
    expect(readdirSync(root).filter(entry => entry.startsWith('leapmux-e2e-dev-'))).toEqual([])
  })

  it('closes the model server it bound when the start fails', async () => {
    // Startup opens the mock listener before it executes the binary. Failure must close it.
    const root = scratchRoot()
    const before = activeServers()
    await startSuiteServer({ binaryPath: join(root, 'no-such-leapmux'), tmpDir: root }).catch(() => {})
    expect(await settledNewServerCount(before)).toBe(0)
  })

  it.skipIf(process.platform === 'win32')('binds the hub\'s local IPC socket from --listen at a path that fits sun_path', async () => {
    // macOS limits a Unix socket path to 104 bytes. Deep workspaces require a separate short path.
    // The repeated --listen flag must supply that path.
    const root = scratchRoot()
    const { binary, recorded } = writeRecordingFakeBinary(root)

    await startSuiteServer({ binaryPath: binary, tmpDir: root }).catch(() => {})
    const record = JSON.parse(readFileSync(recorded, 'utf8')) as FakeRunRecord

    const sockets = record.argv.filter(entry => entry.startsWith('unix:'))
    expect(sockets, `the argv must specify one local IPC socket: ${JSON.stringify(record.argv)}`).toHaveLength(1)
    const socketPath = sockets[0]?.slice('unix:'.length) ?? ''
    // Leave room below the native Unix socket path limit.
    expect(socketPath.length, `socket path is ${socketPath.length} bytes: ${socketPath}`).toBeLessThan(104)
    expect(socketPath.endsWith('/hub.sock')).toBe(true)
    // The IPC socket must use its separate path.
    const dataDirIndex = record.argv.indexOf('-data-dir')
    expect(dataDirIndex).toBeGreaterThanOrEqual(0)
    expect(socketPath.startsWith(record.argv[dataDirIndex + 1] ?? '')).toBe(false)
    // The short path comes from the flag, not from an environment variable.
    expect(record.localListenEnv).toBeNull()
    expect(record.listenEnv).toBeNull()
  })

  it.skipIf(process.platform === 'win32')('asks for an ephemeral TCP port and builds the hub URL from the state file', async () => {
    // Request an ephemeral port to avoid a race between free-port detection and binding.
    // The fake Hub writes its resolved address into the state file.
    const root = scratchRoot()
    const { binary } = writeRecordingFakeBinary(root)

    const failure = await startSuiteServer({ binaryPath: binary, tmpDir: root }).then(
      () => null,
      (error: unknown) => error,
    )
    expect(failure, 'the fake hub serves no API, so the start must fail').not.toBeNull()
    // The failed API wait must use the address from the state file.
    expect(String(failure)).toContain('localhost:44321')
  })
})

interface FakeRunRecord {
  argv: string[]
  listenEnv: string | null
  localListenEnv: string | null
}

/** Write a fake dev executable that records arguments and the resolved Hub address. */
function writeRecordingFakeBinary(root: string): { binary: string, recorded: string } {
  const binary = join(root, 'record-dev')
  const recorded = join(root, 'recorded.json')
  writeFileSync(binary, `#!/bin/sh
if [ "$1" = "dev" ]; then
  node -e '
    const fs = require("fs"), path = require("path");
    const recorded = process.argv[1];
    const args = process.argv.slice(2);
    const dataDir = args[args.indexOf("-data-dir") + 1];
    const unixEntry = args.find(a => a.startsWith("unix:")) ?? null;
    const stateDir = path.join(dataDir, "hub");
    fs.mkdirSync(stateDir, { recursive: true });
    fs.writeFileSync(path.join(stateDir, "state.json"), JSON.stringify({ pid: 1, listen: ["127.0.0.1:44321", unixEntry] }));
    fs.writeFileSync(recorded, JSON.stringify({ argv: args, listenEnv: process.env.LEAPMUX_HUB_LISTEN ?? null, localListenEnv: process.env.LEAPMUX_HUB_LOCAL_LISTEN ?? null }));
  ' ${JSON.stringify(recorded)} "$@"
  exit 1
fi
exit 0
`)
  chmodSync(binary, 0o755)
  return { binary, recorded }
}

/** Read actual TCP server identities. The positive control validates the runtime hook. */
function activeServers(): Set<Server> {
  const active = Reflect.get(process, '_getActiveHandles')
  if (typeof active !== 'function')
    throw new Error('The runtime does not supply process._getActiveHandles.')
  const handles: unknown = Reflect.apply(active, process, [])
  if (!Array.isArray(handles))
    throw new Error('The runtime returned an invalid active handle list.')
  return new Set(handles.filter((handle): handle is Server => handle instanceof Server))
}

/**
 * Wait for new server handles to disappear after close.
 * Compare identities because an earlier closed handle can disappear during this test.
 * Deferred handle removal needs another event-loop turn after the close callback.
 */
async function settledNewServerCount(before: ReadonlySet<Server>, timeoutMs = 30_000): Promise<number> {
  const deadline = Date.now() + timeoutMs
  const addedCount = () => [...activeServers()].filter(server => !before.has(server)).length
  let count = addedCount()
  while (count !== 0 && Date.now() < deadline) {
    await setImmediate()
    count = addedCount()
  }
  return count
}

describe('activeServers', () => {
  it('sees a server open and close, so the leak assertion is not vacuous', async () => {
    const before = activeServers()
    const server = createServer()
    expect(before.has(server)).toBe(false)
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject)
      server.listen(0, '127.0.0.1', () => {
        server.off('error', reject)
        resolve()
      })
    })
    await withCleanup(async () => {
      expect(activeServers().has(server)).toBe(true)
    }, () => new Promise<void>((resolve, reject) => {
      server.close(error => error ? reject(error) : resolve())
    }))
    expect(await settledNewServerCount(before)).toBe(0)
    expect(activeServers().has(server)).toBe(false)
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

describe('ancestorInstructionsReport', () => {
  it('states nothing for a run whose requests held no ancestor instruction text', () => {
    expect(ancestorInstructionsReport(0)).toBe('')
  })

  it.each([
    [1, 'The mock refused 1 model request that held'],
    [3, 'The mock refused 3 model requests that held'],
  ])('states the count of %i refused requests', (count, start) => {
    expect(ancestorInstructionsReport(count)).toMatch(new RegExp(`^${start} the text of an instruction file above the working directory of an agent\\.`))
  })
})
