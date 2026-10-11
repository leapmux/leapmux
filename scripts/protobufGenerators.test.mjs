import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const config = Bun.YAML.parse(readFileSync(join(root, 'buf.gen.yaml'), 'utf8'))
const goModule = readFileSync(join(root, 'backend/go.mod'), 'utf8')
const frontend = JSON.parse(readFileSync(join(root, 'frontend/package.json'), 'utf8'))

function goRuntimeVersion(modulePath) {
  const version = goModule.split('\n').map(line => line.trim().split(/\s+/)).find(fields => fields[0] === modulePath)?.[1]
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
    expect(esVersion).toMatch(/^\^?\d+\.\d+\.\d+$/)
    const expected = [
      { remote: `buf.build/protocolbuffers/go:${goRuntimeVersion('google.golang.org/protobuf')}`, out: 'backend/generated/proto', opt: 'paths=source_relative' },
      { remote: `buf.build/connectrpc/go:${goRuntimeVersion('connectrpc.com/connect')}`, out: 'backend/generated/proto', opt: 'paths=source_relative' },
      { remote: `buf.build/bufbuild/es:v${esVersion.replace(/^\^/, '')}`, out: 'frontend/src/generated/proto', opt: 'target=ts' },
    ]
    expect(config.plugins).toEqual(expected)
  })
})
