import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativeWorkspaceTrustLimit, projectConfigurationWorker } from './nativeWorkspaceTrustLimit'

const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')

describe('exerciseNativeWorkspaceTrustLimit', () => {
  it.each([undefined, {}, { projectConfiguration: {} }, { projectConfiguration: { prepare() {} } }])('rejects a missing actual configuration proof before browser access: %j', async (options) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
      workspaceId: 'config-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The configuration boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The configuration boundary must run before model access.')
      },
    }
    await expect(Reflect.apply(exerciseNativeWorkspaceTrustLimit, undefined, [context, options])).rejects.toThrow('requires an actual native project configuration proof')
  })

  it.each([
    { startup: 'other' },
    { startup: 'failed' },
    { startup: 'failed', startupError: '' },
    { startup: 'failed', startupError: ' \n\t' },
  ])('rejects an invalid failure proof before configuration or browser access: %j', async (startup) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
      workspaceId: 'failed-config-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The failure boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The failure boundary must run before model access.')
      },
    }
    const options = {
      ...startup,
      projectConfiguration: {
        prepare() { throw new Error('The failure boundary must run before configuration writes.') },
        async prove() { throw new Error('The failure boundary must run before configuration proof.') },
      },
    }
    await expect(Reflect.apply(exerciseNativeWorkspaceTrustLimit, undefined, [context, options])).rejects.toThrow(/workspace trust startup|requires the native configuration error/)
  })
})

describe('projectConfigurationWorker', () => {
  const directories: string[] = []
  afterEach(() => {
    for (const directory of directories.splice(0))
      rmSync(directory, { recursive: true, force: true })
  })

  function privateExecutable(name: string): { directory: string, executable: string } {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'project-configuration-worker-'))
    directories.push(directory)
    const executable = join(directory, name)
    writeFileSync(executable, 'This fixture must never run.\n', { mode: 0o755 })
    return { directory, executable }
  }

  it('starts the isolated executable and turns the project configuration back on', () => {
    const { directory, executable } = privateExecutable('native-agent')
    const worker = projectConfigurationWorker({ PATH: directory, NATIVE_DISABLE_PROJECT_CONFIG: 'true' }, { binaryName: 'native-agent', holdWhen: ['acp'] }, 'NATIVE_DISABLE_PROJECT_CONFIG')
    expect(worker.launch).toEqual({ binaryName: 'native-agent', holdWhen: ['acp'], executable })
    expect(Reflect.apply(worker.workerEnvironment, undefined, [])).toEqual({ NATIVE_DISABLE_PROJECT_CONFIG: 'false' })
  })

  it.each([
    { label: 'an absent environment', environment: undefined },
    { label: 'a variable the environment does not set', environment: { NATIVE_DISABLE_PROJECT_CONFIG_TYPO: 'true' } },
    { label: 'a variable that already loads project configuration', environment: { NATIVE_DISABLE_PROJECT_CONFIG: 'false' } },
    { label: 'a variable set to 1', environment: { NATIVE_DISABLE_PROJECT_CONFIG: '1' } },
  ])('refuses $label', ({ environment }) => {
    const { directory } = privateExecutable('native-agent')
    const withPath = environment === undefined ? undefined : { PATH: directory, ...environment }
    expect(() => projectConfigurationWorker(withPath, { binaryName: 'native-agent' }, 'NATIVE_DISABLE_PROJECT_CONFIG')).toThrow('NATIVE_DISABLE_PROJECT_CONFIG')
  })

  it('refuses an absent executable', () => {
    const { directory } = privateExecutable('native-agent')
    expect(() => projectConfigurationWorker({ PATH: directory, NATIVE_DISABLE_PROJECT_CONFIG: 'true' }, { binaryName: 'absent-agent' }, 'NATIVE_DISABLE_PROJECT_CONFIG')).toThrow('executable is absent')
  })
})
