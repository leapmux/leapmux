import type { DevServerHandle } from './devServer'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mintCLITokenForAdmin } from './cli'
import { startSoloServer, startUnseededDevServer, withAdminConfiguredDevServer } from './devServer'
import { stopProcess } from './process'

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

vi.mock('./api', async importOriginal => ({
  ...await importOriginal<typeof import('./api')>(),
  closeTestChannels: vi.fn(async () => {}),
  getUserId: vi.fn(async () => 'admin-user'),
  getWorkerId: vi.fn(async () => 'worker-1'),
  signUpViaAPI: vi.fn(async () => 'leapmux-session=admin'),
}))

vi.mock('./cli', () => ({ mintCLITokenForAdmin: vi.fn() }))

vi.mock('./process', () => ({ stopProcess: vi.fn(async () => {}) }))

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

describe('withAdminConfiguredDevServer', () => {
  /** Mint a CLI credential directory that exists on disk, as the real mint writes one. */
  function mintCredentialDirectory(): string {
    const path = join(state.tmpDir, 'cli-credentials')
    mkdirSync(path, { recursive: true })
    vi.mocked(mintCLITokenForAdmin).mockResolvedValue({ path, hubURL: 'http://localhost:49123' } as Awaited<ReturnType<typeof mintCLITokenForAdmin>>)
    return path
  }

  it('configures the seeded server through the CLI, runs the test, then stops the server and removes the credentials', async () => {
    createScratchRoot()
    vi.mocked(stopProcess).mockClear()
    const credentials = mintCredentialDirectory()
    const steps: string[] = []
    let served: DevServerHandle | undefined
    await withAdminConfiguredDevServer('captcha-test', async (cli, server) => {
      steps.push(`configure ${cli.path === credentials} ${server.adminToken}`)
    }, async (server) => {
      served = server
      steps.push(`use ${server.hubUrl}`)
      expect(stopProcess).not.toHaveBeenCalled()
    })
    expect(steps).toEqual(['configure true leapmux-session=admin', 'use http://localhost:49123'])
    expect(stopProcess).toHaveBeenCalledTimes(1)
    expect(existsSync(credentials)).toBe(false)
    expect(existsSync(served!.dataDir)).toBe(false)
  })

  it('stops the server and removes the credentials when the configuration fails, and never runs the test', async () => {
    createScratchRoot()
    vi.mocked(stopProcess).mockClear()
    const credentials = mintCredentialDirectory()
    const failure = new Error('The captcha setting was refused.')
    const use = vi.fn(async () => {})
    await expect(withAdminConfiguredDevServer('captcha-test', async () => {
      throw failure
    }, use)).rejects.toBe(failure)
    expect(use).not.toHaveBeenCalled()
    expect(stopProcess).toHaveBeenCalledTimes(1)
    expect(existsSync(credentials)).toBe(false)
  })

  it('stops the server when the CLI credential cannot be minted', async () => {
    createScratchRoot()
    vi.mocked(stopProcess).mockClear()
    vi.mocked(mintCLITokenForAdmin).mockRejectedValue(new Error('The mint was refused.'))
    await expect(withAdminConfiguredDevServer('captcha-test', async () => {}, async () => {})).rejects.toThrow('The mint was refused.')
    expect(stopProcess).toHaveBeenCalledTimes(1)
  })
})
