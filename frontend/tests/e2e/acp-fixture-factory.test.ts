import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider, cliSkipFixture, createACPWorkspace, detectACPSkipReason } from './acp-fixture-factory'
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

describe('cliSkipFixture', () => {
  /** Run the fixture callback with a recording test info, as Playwright runs an automatic fixture. */
  async function run(reason: string | null) {
    const [fixture, options] = cliSkipFixture(reason)
    const events: string[] = []
    const testInfo = {
      skip: vi.fn((condition: boolean, description: string) => {
        events.push(`skip:${condition}:${description}`)
        // Playwright ends the test from inside `skip` with a thrown marker when the condition holds.
        if (condition)
          throw new Error(`skipped: ${description}`)
      }),
    }
    const use = vi.fn(async () => {
      events.push('use')
    })
    const outcome = await (fixture as unknown as (args: object, use: () => Promise<void>, info: typeof testInfo) => Promise<void>)({}, use, testInfo)
      .then(() => 'ran', (error: unknown) => (error as Error).message)
    return { options, events, outcome }
  }

  it('registers an automatic fixture, so it runs before the fixtures that start an agent', () => {
    expect(cliSkipFixture(null)[1]).toEqual({ auto: true })
  })

  it('skips with the reason before the test uses any fixture when the CLI is missing', async () => {
    const { events, outcome } = await run('Amp E2E requires the amp CLI on PATH')
    expect(events).toEqual(['skip:true:Amp E2E requires the amp CLI on PATH'])
    expect(outcome).toBe('skipped: Amp E2E requires the amp CLI on PATH')
  })

  it('runs the test when the CLI is present', async () => {
    const { events, outcome } = await run(null)
    expect(events).toEqual(['skip:false:', 'use'])
    expect(outcome).toBe('ran')
  })

  it.each(['', '  \n'])('refuses an empty reason, which would skip without saying why: %j', (reason) => {
    expect(() => cliSkipFixture(reason)).toThrow('needs the reason')
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
