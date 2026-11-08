import { readdirSync, readFileSync } from 'node:fs'
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
  it('fails when macOS cannot access its GUI domain instead of skipping login cases', () => {
    expect(() => nativeCLISelection('darwin', false)).toThrow('macOS native CLI verification requires an accessible GUI domain')
  })
  it('includes both native suites and startup modes on macOS, Windows and graphical Linux', () => {
    for (const platform of ['darwin', 'win32', 'linux']) {
      const selection = nativeCLISelection(platform, true)
      const suites = new RegExp(selection)
      expect(suites.test('TestNativeServiceLifecycle')).toBe(true)
      expect(suites.test('TestNativeServiceCLIConfiguration')).toBe(true)
      expect(suites.test('TestNativeControlAccessProbe')).toBe(false)
    }
  })
  it('selects boot cases from both native suites on headless Linux', () => {
    // Go applies each slash-separated expression to its test-name segment.
    expect(nativeCLISelection('linux', false)).toBe('^TestNativeService(Lifecycle|CLIConfiguration)$/[^/]+/boot$')
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

describe('native CLI per-commit verification', () => {
  it('runs in every native job after building and before publishing compiled caches', () => {
    const workflow = Bun.YAML.parse(readFileSync(new URL('../.github/workflows/ci.yaml', import.meta.url), 'utf8'))
    expect(workflow.on.push.branches).toEqual(['main'])
    expect(workflow.on.pull_request.branches).toEqual(['main'])
    expect(workflow.jobs.linux.strategy.matrix.include.map(entry => entry.arch)).toEqual(['amd64', 'arm64'])
    for (const name of ['linux', 'macos', 'windows']) {
      const job = workflow.jobs[name]
      const checks = job.steps.filter(step => step.run === 'node scripts/native-cli-ci.mjs')
      expect(checks).toHaveLength(1)
      expect(checks[0].if).toBeUndefined()
      const position = job.steps.indexOf(checks[0])
      expect(position).toBeGreaterThan(job.steps.findIndex(step => step.run === 'task build'))
      expect(position).toBeLessThan(job.steps.findIndex(step => step.id === 'go-cache-save'))
      expect(workflow.jobs['required-ci'].needs).toContain(name)
      if (name === 'windows')
        expect(checks[0].shell).toBe('pwsh')
    }
  })
  it('keeps native tests available without a custom Go build tag', () => {
    const source = new URL('../backend/internal/servicelaunch/', import.meta.url)
    for (const file of readdirSync(source).filter(file => file.endsWith('_test.go')))
      expect(readFileSync(new URL(file, source), 'utf8')).not.toMatch(/^\/\/go:build .*\bserviceintegration\b/m)
    const tasks = Bun.YAML.parse(readFileSync(new URL('../Taskfile.yaml', import.meta.url), 'utf8')).tasks
    const native = tasks['test-cli-services']
    expect(native.deps).toContain('assert-service-account-file')
    expect(native.env.LEAPMUX_SERVICE_TEST_ACCOUNT_FILE).toBe('{{.SERVICE_ACCOUNT_FILE}}')
    expect(native.cmds[0]).not.toContain('-tags=')
  })
})
