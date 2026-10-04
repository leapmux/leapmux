import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
import { withAgentWorkspace } from './helpers/workspace'

vi.mock('./helpers/workspace', () => ({ withAgentWorkspace: vi.fn() }))

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'acp-fixture-factory-test-'))
  vi.stubEnv('PATH', directory)
  vi.mocked(withAgentWorkspace).mockReset().mockImplementation(async (_server, _options, use) => use({ workspaceId: 'workspace' }))
})

afterEach(() => {
  vi.unstubAllEnvs()
  rmSync(directory, { recursive: true, force: true })
})

const base = { agentProvider: AgentProvider.GOOSE, workspacePrefix: 'goose-e2e' }

describe('detectACPSkipReason', () => {
  it('skips nothing for a provider that states no CLI to find', () => {
    expect(detectACPSkipReason(base)).toBeNull()
  })

  it('states the default reason for a CLI that the path does not hold', () => {
    expect(detectACPSkipReason({ ...base, cliBinary: 'leapmux-absent-cli' })).toBe('E2E requires leapmux-absent-cli CLI on PATH')
  })

  it('states the skip message of the provider, and the default one for an empty message', () => {
    expect(detectACPSkipReason({ ...base, cliBinary: 'leapmux-absent-cli', skipMessage: 'Install it first' })).toBe('Install it first')
    expect(detectACPSkipReason({ ...base, cliBinary: 'leapmux-absent-cli', skipMessage: '' })).toBe('E2E requires leapmux-absent-cli CLI on PATH')
  })

  // The check runs in the Playwright process with the developer's own HOME, and some
  // agents write their configuration there on every start. This CLI leaves a marker
  // when it runs.
  it.skipIf(process.platform === 'win32')('finds a CLI on the path without running it', () => {
    const marker = join(directory, 'ran')
    const cli = join(directory, 'leapmux-present-cli')
    writeFileSync(cli, `#!/bin/sh\ntouch '${marker}'\n`)
    chmodSync(cli, 0o755)
    expect(detectACPSkipReason({ ...base, cliBinary: 'leapmux-present-cli' })).toBeNull()
    expect(existsSync(marker)).toBe(false)
  })
})

describe('createACPWorkspace', () => {
  const server = { hubUrl: 'http://hub.test', adminToken: 'session', workerId: 'worker' }

  it('hands the working directory of the provider to the workspace', async () => {
    const workingDir = () => '/repository/checkout'
    const use = vi.fn(async () => {})
    await createACPWorkspace(server, { ...base, workingDir }, use)
    expect(withAgentWorkspace).toHaveBeenCalledExactlyOnceWith(server, { provider: AgentProvider.GOOSE, prefix: 'goose-e2e', workingDir }, use)
    expect(use).toHaveBeenCalledExactlyOnceWith({ workspaceId: 'workspace' })
  })

  // The workspace then makes a fresh private directory of the run. An explicit
  // `workingDir: undefined` would state the key, which the option type forbids.
  it('states no working directory for a provider that gives none', async () => {
    await createACPWorkspace(server, base, async () => {})
    const [, options] = vi.mocked(withAgentWorkspace).mock.calls[0]!
    expect(options).toEqual({ provider: AgentProvider.GOOSE, prefix: 'goose-e2e' })
    expect(options).not.toHaveProperty('workingDir')
  })
})
