import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const config = Bun.YAML.parse(readFileSync(join(root, 'buf.gen.yaml'), 'utf8'))
const result = spawnSync('go', ['mod', 'edit', '-json'], { cwd: join(root, 'backend'), encoding: 'utf8', env: { ...process.env, GOWORK: 'off', GOTOOLCHAIN: 'local' } })
expect(result.status, result.stderr).toBe(0)
const requirements = JSON.parse(result.stdout).Require
const frontend = JSON.parse(readFileSync(join(root, 'frontend/package.json'), 'utf8'))

function goRuntimeVersion(modulePath) {
  const matches = requirements.filter(entry => entry.Indirect !== true && entry.Path.replace(/\/v\d+$/, '') === modulePath)
  expect(matches, `The Go runtime family ${modulePath} has one direct requirement.`).toHaveLength(1)
  const version = matches[0].Version
  expect(version, `The Go runtime ${modulePath} declares its version.`).toMatch(/^v\d+\.\d+\.\d+$/)
  return version
}

describe('protobuf generator configuration', () => {
  it('pins every remote generator to an explicit released version', () => {
    expect(config.plugins.length).toBeGreaterThan(0)
    for (const plugin of config.plugins)
      expect(plugin.remote).toMatch(/^buf\.build\/[a-z0-9-]+\/[a-z0-9-]+:v\d+\.\d+\.\d+$/)
  })

  it('generates each used runtime with its declared version and output options', () => {
    const esVersion = frontend.dependencies['@bufbuild/protobuf']
    expect(esVersion).toMatch(/^\d+\.\d+\.\d+$/)
    const expected = [
      { remote: `buf.build/protocolbuffers/go:${goRuntimeVersion('google.golang.org/protobuf')}`, out: 'backend/generated/proto', opt: 'paths=source_relative' },
      { remote: `buf.build/connectrpc/go:${goRuntimeVersion('connectrpc.com/connect')}`, out: 'backend/generated/proto', opt: 'paths=source_relative' },
      { remote: `buf.build/bufbuild/es:v${esVersion.replace(/^\^/, '')}`, out: 'frontend/src/generated/proto', opt: 'target=ts' },
    ]
    expect(config.plugins).toEqual(expected)
  })

  it('keeps the Bun workspace declaration and resolved runtime exact during maintenance', () => {
    const lock = Bun.JSON5.parse(readFileSync(join(root, 'frontend/bun.lock'), 'utf8'))
    const version = frontend.dependencies['@bufbuild/protobuf']
    expect(lock.workspaces[''].dependencies['@bufbuild/protobuf']).toBe(version)
    expect(lock.packages['@bufbuild/protobuf'][0]).toBe(`@bufbuild/protobuf@${version}`)
    expect(Bun.semver.satisfies(`${version.split('.').slice(0, 2).join('.')}.${Number(version.split('.')[2]) + 1}`, version)).toBe(false)
  })
})
