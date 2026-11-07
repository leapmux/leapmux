import { describe, expect, it } from 'bun:test'
import { assertHostedRunner, fixtureSources, nativeCLISelection } from './temporary-native-cli-ci.mjs'

describe('native CLI temporary CI fixture', () => {
  it('refuses workstation and self-hosted environments', () => {
    for (const env of [
      {},
      { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'self-hosted' },
      { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'github-hosted', GITHUB_RUN_ID: '123' },
    ])
      expect(() => assertHostedRunner(env)).toThrow()
  })
  it('selects native CLI service cases without browser or model tests', () => {
    expect(nativeCLISelection(false)).toBe('^TestNativeServiceCLIConfiguration/[^/]+/boot$')
    expect(nativeCLISelection(true)).toBe('^TestNativeServiceCLIConfiguration$')
    for (const platform of ['darwin', 'linux', 'win32']) {
      const sources = fixtureSources(platform)
      expect(sources['main.go']).toContain('atomicfile.WriteFileDurable')
      expect(sources['main.go']).toContain('disposable_environment')
      expect(sources['main.go']).toContain('"credential_reference": nil')
      expect(Object.keys(sources)).toHaveLength(2)
    }
  })
})
