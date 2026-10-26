import { describe, expect, it } from 'bun:test'
import { synchronizeGenerators } from './sync-protobuf-generators.mjs'

const config = '# preserve comments\nplugins:\n  - remote: buf.build/protocolbuffers/go:v1.0.0\n  - remote: buf.build/connectrpc/go:v1.0.0\n  - remote: buf.build/bufbuild/es:v1.0.0\n'
const goModule = 'require (\n google.golang.org/protobuf v1.40.0\n connectrpc.com/connect v1.30.0\n)\n'
const frontend = { dependencies: { '@bufbuild/protobuf': '2.20.0' } }

describe('synchronizeGenerators', () => {
  it('derives independent runtime versions and preserves unrelated configuration', () => {
    expect(synchronizeGenerators(config, goModule, frontend)).toBe(config.replace('go:v1.0.0', 'go:v1.40.0').replace('go:v1.0.0', 'go:v1.30.0').replace('es:v1.0.0', 'es:v2.20.0'))
  })
  it('rejects absent, ranged, or ambiguous runtime and generator entries', () => {
    for (const value of ['', '^2.20.0', null])
      expect(() => synchronizeGenerators(config, goModule, { dependencies: { '@bufbuild/protobuf': value } })).toThrow()
    expect(() => synchronizeGenerators(config, '', frontend)).toThrow()
    expect(() => synchronizeGenerators(config + config, goModule, frontend)).toThrow()
    expect(() => synchronizeGenerators('', goModule, frontend)).toThrow()
  })
})
