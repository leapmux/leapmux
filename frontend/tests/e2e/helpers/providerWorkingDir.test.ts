import type { Page } from '@playwright/test'
import type { AgentServer, openAgentViaAPI } from './api'
import type { NativeAgentOpenOptions, newNativeWorkingDir, openNativeAgent } from './nativeAgentOpen'
import type { openNewAgentFor, reopenFromSessionPicker } from './nativeResume'
import type { ResumeSubject, sessionPickerRepository } from './nativeResumePicker'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderWorkingDir } from './providerWorkingDir'
import type { AgentWorkspace, openProviderAgent, ProviderAgent, ProviderAgentOpenOptions } from './workspace'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, realpathSync, rmSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, expectTypeOf, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { deliberateWorkingDir, gitRepositoryWorkingDir, newProviderWorkingDir } from './providerWorkingDir'

// The run directory of these tests is the scratch directory of the current test.
const runDirectory = vi.hoisted(() => ({ root: '' }))
vi.mock('./runDirectory', () => ({ createTestDirectory: (prefix: string) => mkdtempSync(join(runDirectory.root, prefix)) }))

/** Return the root of the git work tree around `dir`, as git reports it. */
function gitTopLevel(dir: string): string {
  return execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: dir, encoding: 'utf8' }).trim()
}

const pi: ProviderAgent = { provider: AgentProvider.PI, prefix: 'pi-e2e' }

let root: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  root = mkdtempSync(join(scratch, 'provider-working-dir-'))
  runDirectory.root = root
})

afterEach(() => {
  rmSync(root, { recursive: true, force: true })
})

describe('newProviderWorkingDir', () => {
  it('creates a fresh directory of the run with the default prefix for a provider that states no rule', () => {
    const dir = newProviderWorkingDir(pi)
    expect(dirname(dir)).toBe(root)
    expect(basename(dir)).toMatch(/^pi-e2e-wd-/)
    expect(readdirSync(dir)).toEqual([])
    // A plain directory of the run belongs to the work tree around the run, not to a repository of its own.
    expect(gitTopLevel(dir)).not.toBe(realpathSync(dir))
  })

  it('creates a new directory at each call', () => {
    expect(newProviderWorkingDir(pi, 'native-')).not.toBe(newProviderWorkingDir(pi, 'native-'))
  })

  it('creates the directory by the rule of the provider, with the stated prefix', () => {
    const rule = vi.fn((prefix: string) => unitWorkingDir(`/run/${prefix}repository/repo`))
    expect(newProviderWorkingDir({ ...pi, workingDir: rule }, 'native-')).toBe('/run/native-repository/repo')
    expect(rule).toHaveBeenCalledExactlyOnceWith('native-')
    expect(readdirSync(root)).toEqual([])
  })

  it('gives the rule of the provider the default prefix of the agent', () => {
    const rule = vi.fn((prefix: string) => unitWorkingDir(`/run/${prefix}`))
    newProviderWorkingDir({ provider: AgentProvider.KIRO, prefix: 'kiro-e2e', workingDir: rule })
    expect(rule).toHaveBeenCalledExactlyOnceWith('kiro-e2e-wd-')
  })
})

describe('gitRepositoryWorkingDir', () => {
  it('creates a new directory of the run with the prefix, and a repository of its own in it', () => {
    const first = gitRepositoryWorkingDir('agent-wd-')
    const second = gitRepositoryWorkingDir('agent-wd-')
    expect(first).not.toBe(second)
    for (const dir of [first, second]) {
      expect(basename(dir)).toBe('repo')
      expect(dirname(dirname(dir))).toBe(root)
      expect(basename(dirname(dir))).toMatch(/^agent-wd-/)
      // The run directory sits inside the LeapMux checkout, so a plain directory there reports the checkout as its top.
      expect(gitTopLevel(dir)).toBe(realpathSync(dir))
      expect(execFileSync('git', ['log', '--pretty=%s'], { cwd: dir, encoding: 'utf8' }).trim()).toBe('init')
    }
  })

  it('is the rule of a provider that reads configuration from the git repository around its directory', () => {
    const dir = newProviderWorkingDir({ provider: AgentProvider.KIRO, prefix: 'kiro-e2e', workingDir: gitRepositoryWorkingDir })
    expect(basename(dirname(dir))).toMatch(/^kiro-e2e-wd-/)
    expect(gitTopLevel(dir)).toBe(realpathSync(dir))
  })
})

describe('deliberateWorkingDir', () => {
  it('returns the stated directory unchanged, and creates nothing', () => {
    const dir = join(root, 'deliberate', 'repo')
    expect(deliberateWorkingDir(dir, 'The test lays out the directory.')).toBe(dir)
    expect(existsSync(dir)).toBe(false)
  })

  it('refuses a relative directory', () => {
    expect(() => deliberateWorkingDir('repo', 'The test lays out the directory.')).toThrow('must be an absolute path, not "repo"')
  })

  it('refuses an empty directory', () => {
    expect(() => deliberateWorkingDir('', 'The test lays out the directory.')).toThrow('must be an absolute path, not ""')
  })

  it.each(['', ' ', '\n\t'])('refuses a reason that holds no visible text: %j', (reason) => {
    expect(() => deliberateWorkingDir('/run/repo', reason)).toThrow('The deliberate working directory /run/repo must state why no rule of a provider applies.')
  })
})

describe('ProviderWorkingDir', () => {
  // The type checker reads the checks of this block. They do nothing at run time.

  it('cannot come from a plain string', () => {
    expectTypeOf<string>().not.toExtend<ProviderWorkingDir>()
    expectTypeOf<'/run/repo'>().not.toExtend<ProviderWorkingDir>()
    // A directory with the brand is still a path for every function that takes one.
    expectTypeOf<ProviderWorkingDir>().toExtend<string>()
  })

  it('comes from each rule helper and from the deliberate constructor', () => {
    expectTypeOf(newProviderWorkingDir).returns.toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf(gitRepositoryWorkingDir).returns.toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf(deliberateWorkingDir).returns.toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<ReturnType<typeof newNativeWorkingDir>>().toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<ReturnType<typeof sessionPickerRepository>>().toEqualTypeOf<ProviderWorkingDir>()
  })

  it('makes a provider rule return the brand', () => {
    expectTypeOf<ReturnType<NonNullable<ProviderAgent['workingDir']>>>().toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<(prefix: string) => string>().not.toExtend<NonNullable<ProviderAgent['workingDir']>>()
  })

  it('reaches the caller of each open, so another agent can open in the same directory', () => {
    expectTypeOf<Awaited<ReturnType<typeof openProviderAgent>>['workingDir']>().toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<Awaited<ReturnType<typeof openNativeAgent>>['workingDir']>().toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<AgentWorkspace['workingDir']>().toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<ResumeSubject['subjectDir']>().toEqualTypeOf<ProviderWorkingDir>()
  })

  it('is the only directory that an open of a stated provider accepts', () => {
    expectTypeOf<NonNullable<ProviderAgentOpenOptions['workingDir']>>().toEqualTypeOf<ProviderWorkingDir>()
    expectTypeOf<NonNullable<NativeAgentOpenOptions['workingDir']>>().toEqualTypeOf<ProviderWorkingDir>()
    // The function never runs, so no agent opens. Each `@ts-expect-error` fails the type check when its call compiles,
    // and the call with the brand beside it proves that the error comes from the directory alone.
    function opens(
      open: {
        api: typeof openAgentViaAPI
        provider: typeof openProviderAgent
        native: typeof openNativeAgent
        dialog: typeof openNewAgentFor
        picker: typeof reopenFromSessionPicker
      },
      fixtures: { server: AgentServer, page: Page, context: ManagedNativeScenarioContext },
      plain: string,
      ruled: ProviderWorkingDir,
    ): void {
      const { server, page, context } = fixtures
      // @ts-expect-error An agent of a stated provider cannot open in a plain string.
      void open.api(server, 'workspace', plain, { agentProvider: AgentProvider.PI })
      // @ts-expect-error An agent of a stated provider cannot open in the default directory of the Worker.
      void open.api(server, 'workspace', undefined, { agentProvider: AgentProvider.PI })
      void open.api(server, 'workspace', ruled, { agentProvider: AgentProvider.PI })
      // An agent of the Worker default provider opens in any directory.
      void open.api(server, 'workspace', plain, { title: 'Keeper' })
      void open.api(server, 'workspace', undefined)
      void open.api(server, 'workspace', ruled)
      // @ts-expect-error `openProviderAgent` cannot open in a plain string.
      void open.provider(server, 'workspace', pi, { workingDir: plain })
      void open.provider(server, 'workspace', pi, { workingDir: ruled })
      // @ts-expect-error `openNativeAgent` cannot open in a plain string.
      void open.native(context, { workingDir: plain })
      void open.native(context, { workingDir: ruled })
      // @ts-expect-error The New Agent dialog cannot open an agent in a plain string.
      void open.dialog(page, AgentProvider.PI, plain)
      void open.dialog(page, AgentProvider.PI, ruled)
      // @ts-expect-error The session picker cannot reopen a session in a plain string.
      void open.picker(page, { provider: AgentProvider.PI, workingDir: plain, sessionId: 'session' })
      void open.picker(page, { provider: AgentProvider.PI, workingDir: ruled, sessionId: 'session' })
    }
    expectTypeOf(opens).toBeFunction()
  })
})
