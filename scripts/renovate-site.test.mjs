import { describe, expect, it } from 'bun:test'
import { configurationModulePins } from './renovate-site.mjs'

describe('configurationModulePins', () => {
  it('retains configuration-only requirements and selects the longest owning module', () => {
    const metadata = {
      Require: [
        { Path: 'example.com/theme', Version: 'v1.0.0', Indirect: true },
        { Path: 'example.com/theme/extra', Version: 'v2.0.0' },
      ],
    }
    expect(configurationModulePins({
      module: {
        imports: [
          { path: 'example.com/theme/extra/assets' },
          { path: 'example.com/theme' },
          { path: 'example.com/theme' },
        ],
      },
    }, metadata)).toEqual([metadata.Require[1], metadata.Require[0]])
    expect(configurationModulePins({}, metadata)).toEqual([])
    expect(configurationModulePins({
      module: {
        imports: [
          { path: 'missing', disable: true },
        ],
      },
    }, metadata)).toEqual([])
  })
  it('rejects an absent pin, missing path, and malformed import list', () => {
    for (const configuration of [
      {
        module: {
          imports: [
            { path: 'missing' },
          ],
        },
      },
      {
        module: {
          imports: [{}],
        },
      },
      {
        module: { imports: {} },
      },
    ])
      expect(() => configurationModulePins(configuration, {})).toThrow()
  })
})
