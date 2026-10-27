import { describe, expect, it } from 'bun:test'
import { finishGoUpgrades, newModulePath, prepareGoUpgrades, verifyReplacements } from './renovate-go.mjs'

const replacement = version => `replace google.golang.org/genproto => google.golang.org/genproto ${version}\n`
const baseline = replacement('v0.0.0-20260420184626-e10c466a9529')
const upgrade = (extra = {}) => ({ manager: 'gomod', packageFile: 'backend/go.mod', depName: 'example.com/library/v7', newValue: 'v8.1.0', depType: 'require', ...extra })

describe('prepareGoUpgrades', () => {
  it('skips both phases without Go upgrades or graph access', () => {
    const unexpected = () => {
      throw new Error('An unrelated update accessed the Go graph.')
    }
    for (const upgrades of [[], [{ manager: 'bun', packageFile: 'frontend/package.json' }]]) {
      const options = { root: '/fixture', trusted: '/trusted', run: unexpected, read: unexpected, write: unexpected }
      const state = prepareGoUpgrades(upgrades, options)
      expect(state).toEqual({ hasWorkspaceGo: false, hasSiteGo: false })
      finishGoUpgrades(state, options)
    }
  })
})

describe('newModulePath', () => {
  it('handles version suffixes, first-major paths, and incompatible modules', () => {
    expect(newModulePath(upgrade())).toBe('example.com/library/v8')
    expect(newModulePath(upgrade({ depName: 'gopkg.in/yaml.v2', newValue: 'v3.0.1' }))).toBe('gopkg.in/yaml.v3')
    expect(newModulePath(upgrade({ depName: 'gopkg.in/tomb.v2-unstable', newValue: 'v3.0.0' }))).toBe('gopkg.in/tomb.v3')
    expect(newModulePath(upgrade({ depName: 'example.com/library', newValue: 'v2.0.0' }))).toBe('example.com/library/v2')
    expect(newModulePath(upgrade({ depName: 'example.com/library', newValue: 'v2.0.0+incompatible' }))).toBe('example.com/library')
    expect(newModulePath(upgrade({ depName: 'google.golang.org/genproto', newValue: 'v0.0.0-20261020000000-abcdefabcdef' }))).toBe('google.golang.org/genproto')
  })
})

describe('verifyReplacements', () => {
  it('requires one equal replacement in each workspace module', () => {
    expect(() => verifyReplacements([baseline, baseline])).not.toThrow()
    for (const texts of [[baseline, ''], [baseline, baseline + baseline], [baseline, replacement('v0.0.0-other')]])
      expect(() => verifyReplacements(texts)).toThrow()
  })
})
