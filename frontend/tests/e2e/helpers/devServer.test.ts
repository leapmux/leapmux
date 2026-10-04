import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { startSoloServer, startUnseededDevServer } from './devServer'

const state = vi.hoisted(() => ({
  tmpDir: '',
  args: [] as string[],
  waitedURL: '',
}))

vi.mock('./server', async importOriginal => ({
  ...await importOriginal<typeof import('./server')>(),
  getGlobalState: () => ({ binaryPath: 'mock-leapmux', tmpDir: state.tmpDir }),
  hubSpawnEnv: () => ({}),
  findFreePort: () => { throw new Error('the hub must assign its own port') },
  waitForHubReady: async (url: string) => { state.waitedURL = url },
}))

vi.mock('./processRegistry', async (importOriginal) => {
  const { EventEmitter } = await import('node:events')
  const { mkdirSync, writeFileSync } = await import('node:fs')
  const { join } = await import('node:path')
  return {
    ...await importOriginal<typeof import('./processRegistry')>(),
    spawnTestProcess: (_binary: string, args: string[]) => {
      state.args = args
      const dataDir = args[args.indexOf('-data-dir') + 1]
      if (!dataDir)
        throw new Error('the hub needs its data directory')
      const stateDir = join(dataDir, 'hub')
      mkdirSync(stateDir, { recursive: true })
      const requested = args[args.indexOf('-listen') + 1] ?? ''
      const host = requested.slice(0, requested.lastIndexOf(':'))
      writeFileSync(join(stateDir, 'state.json'), JSON.stringify({ listen: [`${host}:49123`] }))
      return Object.assign(new EventEmitter(), {
        stdout: { resume() {} },
        stderr: { resume() {} },
        exitCode: null,
        signalCode: null,
      })
    },
  }
})

const roots: string[] = []

function createScratchRoot(): void {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  state.tmpDir = mkdtempSync(join(scratch, 'dev-server-test-'))
  roots.push(state.tmpDir)
}

afterEach(() => {
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
  state.tmpDir = ''
  state.args = []
  state.waitedURL = ''
})

describe('startUnseededDevServer', () => {
  it('uses a kernel-assigned port and the resolved address from the hub state file', async () => {
    createScratchRoot()

    const server = await startUnseededDevServer()

    expect(state.args.slice(0, 3)).toEqual(['dev', '-listen', '127.0.0.1:0'])
    expect(server.hubUrl).toBe('http://localhost:49123')
    expect(state.waitedURL).toBe(server.hubUrl)
  })

  it('uses the resolved Solo bind address while the browser stays on loopback', async () => {
    createScratchRoot()

    const server = await startSoloServer({ listenHost: '0.0.0.0' })

    expect(state.args.slice(0, 3)).toEqual(['solo', '-listen', '0.0.0.0:0'])
    expect(server.listen).toBe('0.0.0.0:49123')
    expect(server.hubUrl).toBe('http://127.0.0.1:49123')
    expect(state.waitedURL).toBe(server.hubUrl)
  })
})
