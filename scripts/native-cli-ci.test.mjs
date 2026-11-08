import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'bun:test'
import { assertHostedRunner, nativeCLISelection, withFixtureCleanup } from './native-cli-ci.mjs'

const hosted = {
  GITHUB_ACTIONS: 'true',
  RUNNER_ENVIRONMENT: 'github-hosted',
  GITHUB_RUN_ID: '123',
  GITHUB_RUN_ATTEMPT: '1',
  GITHUB_REPOSITORY: 'leapmux/leapmux',
  RUNNER_TEMP: path.resolve('.tmp'),
}

describe('assertHostedRunner', () => {
  it('accepts complete disposable runner metadata', () => {
    expect(() => assertHostedRunner(hosted)).not.toThrow()
  })
  it('refuses workstation, self-hosted, incomplete and malformed environments', () => {
    expect(() => assertHostedRunner({})).toThrow()
    for (const [key, value] of [
      ['GITHUB_ACTIONS', 'false'],
      ['RUNNER_ENVIRONMENT', 'self-hosted'],
      ['GITHUB_RUN_ID', ''],
      ['GITHUB_RUN_ID', '0'],
      ['GITHUB_RUN_ID', '../123'],
      ['GITHUB_RUN_ATTEMPT', '-1'],
      ['GITHUB_REPOSITORY', 'invalid'],
      ['RUNNER_TEMP', 'relative'],
    ])
      expect(() => assertHostedRunner({ ...hosted, [key]: value })).toThrow()
  })
})

describe('nativeCLISelection', () => {
  it('selects login only when the native desktop session exists', () => {
    const boot = new RegExp(nativeCLISelection(false))
    const desktop = new RegExp(nativeCLISelection(true))
    expect(boot.test('TestNativeServiceCLIConfiguration/dev/boot')).toBe(true)
    expect(boot.test('TestNativeServiceCLIConfiguration/dev/login')).toBe(false)
    expect(boot.test('TestNativeServiceLifecycle/dev/boot')).toBe(false)
    expect(desktop.test('TestNativeServiceCLIConfiguration')).toBe(true)
    expect(desktop.test('TestNativeServiceLifecycle')).toBe(false)
  })
})

describe('withFixtureCleanup', () => {
  it('prepares, verifies and removes the credential and file in order', () => {
    const calls = []
    withFixtureCleanup(...[
      'prepare',
      'verify',
      'cleanup',
      'remove',
    ].map(step => () => calls.push(step)))
    expect(calls).toEqual([
      'prepare',
      'verify',
      'cleanup',
      'remove',
    ])
  })
  it('cleans partial preparation and never verifies it', () => {
    const failure = new Error('prepare')
    const calls = []
    expect(() => withFixtureCleanup(() => {
      throw failure
    }, () => calls.push('verify'), () => calls.push('cleanup'), () => calls.push('remove'))).toThrow(failure)
    expect(calls).toEqual(['cleanup', 'remove'])
  })
  it('retains the verification failure and every cleanup failure', () => {
    const failures = ['verify', 'cleanup', 'remove'].map(message => new Error(message))
    let actual
    try {
      withFixtureCleanup(() => {}, ...failures.map(failure => () => {
        throw failure
      }))
    }
    catch (error) {
      actual = error
    }
    expect(actual).toBeInstanceOf(AggregateError)
    expect(actual.errors).toEqual(failures)
  })
  it('reports cleanup failure after successful verification and still removes files', () => {
    let removed = false
    const failure = new Error('cleanup')
    expect(() => withFixtureCleanup(() => {}, () => {}, () => {
      throw failure
    }, () => {
      removed = true
    })).toThrow(failure)
    expect(removed).toBe(true)
  })
})

describe('native CLI weekly workflow', () => {
  it('runs weekly and manually on four native runners with read-only compiled caches', () => {
    const workflow = Bun.YAML.parse(readFileSync(new URL('../.github/workflows/native-cli.yaml', import.meta.url), 'utf8'))
    expect(Object.keys(workflow.on).sort()).toEqual(['schedule', 'workflow_call', 'workflow_dispatch'])
    expect(workflow.on.schedule).toEqual([
      { cron: '43 18 * * 1' },
    ])
    expect(workflow.permissions).toEqual({ contents: 'read', actions: 'read' })
    expect(Object.keys(workflow.jobs).sort()).toEqual(['linux', 'macos', 'windows'])
    expect(workflow.jobs.linux.strategy.matrix.runner_suffix).toEqual(['', '-arm'])
    expect(workflow.jobs.linux.strategy['fail-fast']).toBe(false)
    expect(workflow.jobs.windows.defaults.run.shell).toBe('pwsh')
    for (const job of Object.values(workflow.jobs)) {
      expect(job['timeout-minutes']).toBe(45)
      expect(job.steps.at(-1).run).toBe('node scripts/native-cli-ci.mjs')
      const setup = job.steps.find(step => step.uses === './.github/actions/setup-build')
      expect(setup.with['setup-desktop']).toBe('false')
      expect(setup.with['save-build-caches']).toBe('false')
      expect(job.steps[0].with['persist-credentials']).toBe(false)
    }
  })
})
