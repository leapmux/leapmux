import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { synchronizeGenerators } from './sync-protobuf-generators.mjs'

const config = '# preserve comments\nplugins:\n  - remote: buf.build/protocolbuffers/go:v1.0.0\n  - remote: buf.build/connectrpc/go:v1.0.0\n  - remote: buf.build/bufbuild/es:v1.0.0\n'
const goMetadata = {
  Require: [
    { Path: 'google.golang.org/protobuf', Version: 'v1.40.0' },
    { Path: 'connectrpc.com/connect', Version: 'v1.30.0' },
  ],
}
const frontend = {
  dependencies: { '@bufbuild/protobuf': '2.20.0' },
}
const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const directory of fixtures.splice(0)) rmSync(directory, { recursive: true, force: true })
})
function readMetadata(source) {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(root, '.tmp/generator-metadata-'))
  fixtures.push(directory)
  writeFileSync(resolve(directory, 'go.mod'), `module example.com/fixture\ngo 1.27.1\n${source}`)
  const result = spawnSync('go', ['mod', 'edit', '-json'], {
    cwd: directory,
    encoding: 'utf8',
    env: {
      ...process.env,
      GOWORK: 'off',
      GOTOOLCHAIN: 'local',
      GOPROXY: 'off',
    },
  })
  expect(result.status, result.stderr).toBe(0)
  return JSON.parse(result.stdout)
}

describe('synchronizeGenerators', () => {
  it('selects direct runtime major paths instead of an older indirect runtime', () => {
    for (const indirect of [true, false]) {
      const requirements = [
        { Path: 'google.golang.org/protobuf/v2', Version: 'v2.3.0' },
        { Path: 'connectrpc.com/connect/v2', Version: 'v2.4.0' },
        ...(indirect
          ? goMetadata.Require.map(entry => ({ ...entry, Indirect: true }))
          : []),
      ]
      const result = synchronizeGenerators(config, { Require: requirements }, frontend)
      expect(result).toContain('buf.build/protocolbuffers/go:v2.3.0')
      expect(result).toContain('buf.build/connectrpc/go:v2.4.0')
    }
  })
  it('uses structured Go metadata for standalone and block directives without registry access', () => {
    const standalone = readMetadata('require google.golang.org/protobuf/v2 v2.3.0\nrequire connectrpc.com/connect/v2 v2.4.0\n')
    const block = readMetadata('require (\n google.golang.org/protobuf/v2 v2.3.0\n connectrpc.com/connect/v2 v2.4.0\n)\n')
    expect(synchronizeGenerators(config, standalone, frontend)).toBe(synchronizeGenerators(config, block, frontend))
    expect(synchronizeGenerators(config, standalone, frontend)).toContain('buf.build/protocolbuffers/go:v2.3.0')
  })
  it('derives independent runtime versions and preserves unrelated configuration', () => {
    expect(synchronizeGenerators(config, goMetadata, frontend)).toBe(config.replace('go:v1.0.0', 'go:v1.40.0').replace('go:v1.0.0', 'go:v1.30.0').replace('es:v1.0.0', 'es:v2.20.0'))
    const quoted = config.replace('remote: buf.build/protocolbuffers/go:v1.0.0', 'remote: "buf.build/protocolbuffers/go:v1.0.0" # preserve')
    const commented = `# remote: buf.build/protocolbuffers/go:v0.0.0\n${quoted}`
    const result = synchronizeGenerators(commented, goMetadata, frontend)
    expect(result).toContain('# remote: buf.build/protocolbuffers/go:v0.0.0')
    expect(result).toContain('remote: "buf.build/protocolbuffers/go:v1.40.0" # preserve')
  })
  it('rejects absent, indirect-only, ambiguous, and inconsistent runtime requirements', () => {
    for (const metadata of [
      {},
      {
        Require: goMetadata.Require.map(entry => ({ ...entry, Indirect: true })),
      },
      {
        Require: [
          ...goMetadata.Require,
          { Path: 'google.golang.org/protobuf/v2', Version: 'v2.3.0' },
        ],
      },
    ])
      expect(() => synchronizeGenerators(config, metadata, frontend)).toThrow('one direct runtime')
    for (const [Path, Version] of [
      ['google.golang.org/protobuf/v2', 'v3.0.0'],
      ['google.golang.org/protobuf/v1', 'v1.40.0'],
      ['google.golang.org/protobuf', 'v2.3.0'],
    ]) {
      expect(() => synchronizeGenerators(config, {
        Require: [
          { Path, Version },
          goMetadata.Require[1],
        ],
      }, frontend)).toThrow('path and version disagree')
    }
    expect(() => synchronizeGenerators(config, {
      Require: [
        { Path: 'google.golang.org/protobuf', Version: 'v1.40.0-beta' },
        goMetadata.Require[1],
      ],
    }, frontend)).toThrow('exact runtime version')
  })
  it('rejects ranged frontend pins and missing or duplicate active generator entries', () => {
    for (const value of ['', '^2.20.0', null]) {
      expect(() => synchronizeGenerators(config, goMetadata, {
        dependencies: { '@bufbuild/protobuf': value },
      })).toThrow('exact version')
    }
    expect(() => synchronizeGenerators(config.replace('plugins:', 'plugins:\n  - remote: buf.build/protocolbuffers/go:v1.0.0'), goMetadata, frontend)).toThrow('one generator entry')
    expect(() => synchronizeGenerators('plugins: []\n', goMetadata, frontend)).toThrow('one generator entry')
  })
})
