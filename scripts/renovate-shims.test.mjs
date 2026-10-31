import { chmodSync, mkdirSync, readFileSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { findRealTool, toolShimPaths, writeToolShims } from './renovate-shims.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('renovate-shims')

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
    'frontend/bun.lock': {
      'x@1.1.0': ['x-core@1.1.0'],
    },
    '.github/renovate/bun.lock': {},
    'desktop/rust/Cargo.lock': {
      'fix@2.0.1': ['pin@1.0.5'],
    },
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
      authorized: prepared.authorized,
      cratesIndex: 'https://index.crates.io',
      invocationLog: paths.invocationLog,
    })
  })
  it('never resolves a shim of an earlier run that PATH spells through a symlink', () => {
    const directory = fixture()
    const paths = toolShimPaths(directory)
    const bun = fakeTool(join(directory, 'tools'), 'bun')
    fakeTool(join(directory, 'tools'), 'cargo')
    const env = {
      PATH: join(directory, 'tools'),
    }
    writeToolShims({
      root: directory,
      policy,
      prepared,
      env,
    })
    // A second spelling of the shim directory, first on PATH, as a symlinked checkout gives.
    symlinkSync(paths.bin, join(directory, 'linked-bin'))
    const written = writeToolShims({
      root: directory,
      policy,
      prepared,
      env: {
        PATH: [
          join(directory, 'linked-bin'),
          join(directory, 'tools'),
        ].join(delimiter),
      },
    })
    expect(written.document.realTools.bun).toBe(bun)
    expect(readFileSync(join(paths.bin, 'bun'), 'utf8')).toStartWith(`#!/bin/sh\nexec '${bun}' `)
  })
  it('writes the crate index that the caller passes, which the aged index reads', () => {
    const directory = fixture()
    fakeTool(join(directory, 'tools'), 'bun')
    fakeTool(join(directory, 'tools'), 'cargo')
    const { document } = writeToolShims({
      root: directory,
      policy,
      prepared,
      cratesIndex: 'http://127.0.0.1:1/index',
      env: {
        PATH: join(directory, 'tools'),
      },
    })
    expect(document.cratesIndex).toBe('http://127.0.0.1:1/index')
  })
  it('removes every file and the invocation records of an earlier run', () => {
    const directory = fixture()
    const paths = toolShimPaths(directory)
    mkdirSync(join(paths.directory, 'crates'), { recursive: true })
    writeFileSync(join(paths.directory, 'crates/stale.json'), '[]')
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
    expect(() => statSync(join(paths.directory, 'crates'))).toThrow()
    expect(() => statSync(paths.invocationLog)).toThrow()
  })
  it('rejects a prepared file without authorized fixes or with a malformed map, and a missing tool', () => {
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
    // The map crossed a JSON file, so the shims must not trust its shape.
    for (const authorized of [
      { npm: {} },
      { 'backend/go.mod': {} },
      {
        'frontend/bun.lock': { 'x@1.0.0': 'x-core@1.0.0' },
      },
      null,
    ]) {
      expect(() => writeToolShims({
        root: directory,
        policy,
        prepared: { authorized },
        env: {
          PATH: join(directory, 'tools'),
        },
      }), JSON.stringify(authorized)).toThrow(/unexpected lockfile|must list package identities|must map each lockfile path/)
    }
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
  it('skips an excluded directory under any spelling that resolves to it', () => {
    const directory = fixture()
    fakeTool(join(directory, 'excluded'), 'bun')
    symlinkSync(join(directory, 'excluded'), join(directory, 'alias'))
    const bun = fakeTool(join(directory, 'real'), 'bun')
    expect(findRealTool('bun', {
      path: [
        join(directory, 'alias'),
        join(directory, 'real'),
      ].join(delimiter),
      exclude: [
        join(directory, 'excluded'),
      ],
    })).toBe(bun)
  })
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
