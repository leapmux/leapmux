import { spawnSync } from 'node:child_process'
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { delimiter, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { benchmarkRebases, postUpgradeEnvironment, representativeQueueDuration, selectRebaseUpgrades } from './renovate-rebase-benchmark.mjs'
import { toolShimPaths } from './renovate-shims.mjs'
import { git, gitRepository, scratchDirectories } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const upgrades = [
  {
    manager: 'gomod',
    packageFile: 'backend/go.mod',
    depName: 'example.com/library',
    newValue: 'v1.1.0',
    branchName: 'renovate/go-security',
  },
]

function repository() {
  const path = scratch.create('rebase-test')
  gitRepository(path, {
    files: {
      'README.md': 'Original base\n',
      '.gitignore': '.tmp/\n',
      '.gitattributes': '*.sum binary\n',
      'backend/go.mod': 'module example.com/fixture\n',
      'backend/go.sum': 'Original checksum\n',
    },
  })
  return {
    path,
    git: (...args) => git(path, args),
  }
}

describe('representativeQueueDuration', () => {
  it('includes fixture time and rejects invalid or excessive totals', () => {
    expect(representativeQueueDuration(500000, 300000)).toBe(800000)
    expect(representativeQueueDuration(0, 0)).toBe(0)
    expect(representativeQueueDuration(40 * 60000, 0)).toBe(40 * 60000)
    for (const values of [
      [-1, 0],
      [0, Infinity],
      [NaN, 0],
      [0.5, 0],
      [40 * 60000, 1],
    ])
      expect(() => representativeQueueDuration(...values)).toThrow()
  })
})

describe('selectRebaseUpgrades', () => {
  const env = {
    msg: 'Env config',
    config: { dryRun: 'full' },
  }
  const record = {
    msg: 'Created post-upgrade commands data file.',
    branch: 'renovate/go-security',
    dataFileContent: JSON.stringify(upgrades),
  }
  it('selects only a successful workspace Go proposal from a verified full dry run', () => {
    expect(selectRebaseUpgrades([env, record], [
      {
        status: 'passed',
        branches: [record.branch],
      },
    ])).toEqual(upgrades)
    expect(selectRebaseUpgrades([env, record], [
      {
        status: 'failed',
        branches: [record.branch],
      },
    ])).toBeNull()
    expect(selectRebaseUpgrades([env], [])).toBeNull()
    for (const records of [
      [],
      [
        {
          ...env,
          config: { dryRun: null },
        },
      ],
    ])
      expect(() => selectRebaseUpgrades(records, [])).toThrow('verified full dry run')
    expect(() => selectRebaseUpgrades([
      env,
      { ...record, dataFileContent: 'invalid' },
    ], [
      {
        status: 'passed',
        branches: [record.branch],
      },
    ])).toThrow()
  })
})

describe('rebase measurement entry point', () => {
  it('records a healthy empty queue without executing a fixture', () => {
    const { path } = repository()
    mkdirSync(resolve(path, '.tmp'))
    writeFileSync(resolve(path, '.tmp/renovate-execution.jsonl'), `${JSON.stringify({ code: 0, timedOut: false, durationMs: 100 })}\n`)
    writeFileSync(resolve(path, '.tmp/renovate-cli.log'), `${JSON.stringify({
      msg: 'Env config',
      config: { dryRun: 'full' },
    })}\n${JSON.stringify({ msg: 'Repository finished' })}\n`)
    writeFileSync(resolve(path, '.tmp/renovate-timings.jsonl'), '')
    const result = spawnSync(process.execPath, [
      resolve(root, 'scripts/renovate-rebase-benchmark.mjs'),
    ], {
      cwd: path,
      encoding: 'utf8',
      env: {
        ...process.env,
        GITHUB_ACTIONS: 'true',
        GITHUB_REPOSITORY: 'leapmux/leapmux',
        GITHUB_REF: 'refs/heads/main',
        RENOVATE_DRY_RUN: 'full',
        RENOVATE_TRUSTED_ROOT: path,
      },
    })
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(readFileSync(resolve(path, '.tmp/renovate-rebase-benchmark.json'), 'utf8')).status).toBe('skipped')
    expect(readdirSync(resolve(path, '.tmp')).some(entry => entry.startsWith('checkout-'))).toBe(false)
  })
})

describe('benchmarkRebases', () => {
  it('rebases actual fixture history and regenerates binary proposals without changing the source', async () => {
    const { path, git } = repository()
    const sourceHead = git('rev-parse', 'HEAD')
    const sourceRefs = git('for-each-ref', '--format=%(refname):%(objectname)', 'refs/heads')
    const outputRoot = resolve(path, '.tmp/measurements')
    const labels = []
    const result = await benchmarkRebases({
      sourceRoot: path,
      upgrades,
      outputRoot,
      execute: (fixture, _trusted, metadata) => {
        const label = metadata[0].branchName
        labels.push(label)
        if (label !== 'benchmark/initial')
          expect(readFileSync(resolve(fixture, 'README.md'), 'utf8')).toContain('Rebase validation base.')
        if (label === 'benchmark/go-import-base')
          expect(readFileSync(resolve(fixture, 'backend/internal/hub/frontend/renovate_rebase_fixture.go'), 'utf8')).toContain('github.com/google/shlex')
        writeFileSync(resolve(fixture, 'backend/go.mod'), 'module example.com/fixture\nrequire example.com/library v1.1.0\n')
        writeFileSync(resolve(fixture, 'backend/go.sum'), new Uint8Array([0, 255, 1]))
      },
    })
    expect(result.status).toBe('passed')
    expect(result.cases.map(entry => entry.label)).toEqual(['readme-base', 'go-import-base'])
    expect(labels).toEqual(['benchmark/initial', 'benchmark/readme-base', 'benchmark/go-import-base'])
    expect(git('rev-parse', 'HEAD')).toBe(sourceHead)
    expect(git('for-each-ref', '--format=%(refname):%(objectname)', 'refs/heads')).toBe(sourceRefs)
    expect(git('status', '--porcelain')).toBe('')
    expect(readFileSync(resolve(path, 'backend/go.mod'), 'utf8')).not.toContain('v1.1.0')
    expect(readdirSync(outputRoot)).toEqual([])
  })
  it('cleans the private checkout and propagates artifact failures', async () => {
    const { path } = repository()
    const outputRoot = resolve(path, '.tmp/measurements')
    await expect(benchmarkRebases({
      sourceRoot: path,
      upgrades,
      outputRoot,
      execute: () => {
        throw new Error('Artifact processing failed.')
      },
    })).rejects.toThrow('Artifact processing failed.')
    expect(readdirSync(outputRoot)).toEqual([])
  })
})

describe('postUpgradeEnvironment', () => {
  it('puts the tool shims of the dry run first on PATH, as Renovate does, and only when they exist', () => {
    const trusted = scratch.create('rebase-shims')
    const base = { PATH: '/usr/bin', CI: 'true' }
    expect(postUpgradeEnvironment(trusted, '/data.json', base)).toEqual({
      PATH: '/usr/bin',
      CI: 'true',
      RENOVATE_TRUSTED_ROOT: trusted,
      RENOVATE_POST_UPGRADE_COMMAND_DATA_FILE: '/data.json',
    })
    const shims = toolShimPaths(trusted).bin
    mkdirSync(shims, { recursive: true })
    writeFileSync(resolve(shims, 'bun'), '')
    expect(postUpgradeEnvironment(trusted, '/data.json', base).PATH).toBe([shims, '/usr/bin'].join(delimiter))
  })
})
