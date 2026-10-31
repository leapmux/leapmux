import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { findRealTool, toolShimPaths, writeToolShims } from './renovate-shims.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})

function fixture() {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(root, '.tmp/renovate-shims-'))
  fixtures.push(directory)
  return directory
}

function fakeTool(directory, name) {
  mkdirSync(directory, { recursive: true })
  const file = join(directory, name)
  writeFileSync(file, '#!/bin/sh\nexit 0\n')
  chmodSync(file, 0o755)
  return file
}

const policy = { minimumReleaseAge: '3 days', minimumReleaseAgeBuffer: null }
const prepared = {
  authorized: {
    npm: {
      'x@1.1.0': ['x-core@1.1.0'],
    },
    crates: {
      'fix@2.0.1': ['pin@1.0.5'],
    },
    go: { 'example.com/lib@v1.0.1': [] },
  },
}

describe('toolShimPaths', () => {
  it('places every shim file under the trusted checkout', () => {
    const paths = toolShimPaths('/trusted')
    expect(paths).toEqual({
      directory: '/trusted/.tmp/renovate-shims',
      bin: '/trusted/.tmp/renovate-shims/bin',
      policy: '/trusted/.tmp/renovate-shims/policy.json',
      invocationLog: '/trusted/.tmp/renovate-shims/invocations.jsonl',
      crates: '/trusted/.tmp/renovate-shims/crates',
    })
    expect(() => toolShimPaths('relative')).toThrow('absolute')
  })
})

// The shims are POSIX shell scripts. Renovate runs only on Linux.
describe.skipIf(process.platform === 'win32')('writeToolShims', () => {
  it('writes executable shims that call the real Bun with absolute paths, and skips its own directory', () => {
    const directory = fixture()
    const paths = toolShimPaths(directory)
    // A shim left by an earlier run must never resolve as the real tool.
    fakeTool(paths.bin, 'bun')
    const bun = fakeTool(join(directory, 'tools/bun'), 'bun')
    const cargo = fakeTool(join(directory, 'tools/cargo'), 'cargo')
    const env = {
      PATH: [
        paths.bin,
        'relative/bin',
        join(directory, 'tools/bun'),
        join(directory, 'tools/cargo'),
      ].join(delimiter),
    }
    const written = writeToolShims({
      root: directory,
      policy,
      prepared,
      now: Date.parse('2026-10-31T22:20:38.123Z'),
      env,
    })
    expect(written.document.realTools).toEqual({ bun, cargo })
    for (const tool of ['bun', 'cargo']) {
      const file = join(paths.bin, tool)
      expect(statSync(file).mode & 0o777).toBe(0o755)
      expect(readFileSync(file, 'utf8')).toBe(`#!/bin/sh\nexec '${bun}' '${resolve(import.meta.dirname, 'renovate-tool-shim.mjs')}' ${tool} '${paths.policy}' "$@"\n`)
    }
  })
  it('writes the policy with the cutoff, the age in seconds, and the npm and crates roots only', () => {
    const directory = fixture()
    const env = {
      PATH: [
        join(directory, 'tools'),
      ].join(delimiter),
    }
    fakeTool(join(directory, 'tools'), 'bun')
    fakeTool(join(directory, 'tools'), 'cargo')
    writeToolShims({
      root: directory,
      policy,
      prepared,
      now: Date.parse('2026-10-31T22:20:38.123Z'),
      env,
    })
    const paths = toolShimPaths(directory)
    expect(JSON.parse(readFileSync(paths.policy, 'utf8'))).toEqual({
      version: 1,
      cutoff: '2026-10-28T22:20:38Z',
      ageSeconds: 259200,
      realTools: {
        bun: join(directory, 'tools/bun'),
        cargo: join(directory, 'tools/cargo'),
      },
      authorized: { npm: prepared.authorized.npm, crates: prepared.authorized.crates },
      cratesApi: 'https://crates.io/api/v1',
      maxRepairRounds: 10,
      invocationLog: paths.invocationLog,
    })
  })
  it('removes the crate cache and the invocation records of an earlier run', () => {
    const directory = fixture()
    const paths = toolShimPaths(directory)
    mkdirSync(paths.crates, { recursive: true })
    writeFileSync(join(paths.crates, 'stale.json'), '[]')
    writeFileSync(paths.invocationLog, '{}\n')
    fakeTool(join(directory, 'tools'), 'bun')
    fakeTool(join(directory, 'tools'), 'cargo')
    writeToolShims({
      root: directory,
      policy,
      prepared,
      env: {
        PATH: join(directory, 'tools'),
      },
    })
    expect(() => statSync(join(paths.crates, 'stale.json'))).toThrow()
    expect(() => statSync(paths.invocationLog)).toThrow()
  })
  it('rejects a prepared file without authorized fixes and a missing tool', () => {
    const directory = fixture()
    fakeTool(join(directory, 'tools'), 'bun')
    expect(() => writeToolShims({
      root: directory,
      policy,
      prepared: {},
      env: {
        PATH: join(directory, 'tools'),
      },
    })).toThrow('lacks the authorized fixes')
    expect(() => writeToolShims({
      root: directory,
      policy,
      prepared,
      env: {
        PATH: join(directory, 'tools'),
      },
    })).toThrow('Cannot find cargo')
  })
})

describe.skipIf(process.platform === 'win32')('findRealTool', () => {
  it('skips a directory that is not absolute, a missing file, and a file that is not executable', () => {
    const directory = fixture()
    mkdirSync(join(directory, 'plain'))
    writeFileSync(join(directory, 'plain/bun'), '')
    const bun = fakeTool(join(directory, 'real'), 'bun')
    expect(findRealTool('bun', {
      path: [
        'relative',
        join(directory, 'missing'),
        join(directory, 'plain'),
        join(directory, 'real'),
      ].join(delimiter),
    })).toBe(bun)
  })
})
