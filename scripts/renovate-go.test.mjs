import { describe, expect, it } from 'bun:test'
import { finishGoUpgrades, prepareGoUpgrades, verifyReplacements } from './renovate-go.mjs'

const replacement = version => `replace google.golang.org/genproto => google.golang.org/genproto ${version}\n`
const baseline = replacement('v0.0.0-20260420184626-e10c466a9529')
describe('prepareGoUpgrades', () => {
  it('skips both phases without Go upgrades or graph access', () => {
    const unexpected = () => {
      throw new Error('An unrelated update accessed the Go graph.')
    }
    for (const upgrades of [
      [],
      [
        { manager: 'bun', packageFile: 'frontend/package.json' },
      ],
    ]) {
      const options = {
        root: '/fixture',
        trusted: '/trusted',
        run: unexpected,
        read: unexpected,
        write: unexpected,
      }
      const state = prepareGoUpgrades(upgrades, options)
      expect(state).toEqual({ hasWorkspaceGo: false, hasSiteGo: false })
      finishGoUpgrades(state, options)
    }
  })
})

describe('verifyReplacements', () => {
  it('requires one equal replacement in each workspace module', () => {
    expect(() => verifyReplacements([baseline, baseline])).not.toThrow()
    for (const texts of [
      [baseline, ''],
      [baseline, baseline + baseline],
      [
        baseline,
        replacement('v0.0.0-other'),
      ],
    ])
      expect(() => verifyReplacements(texts)).toThrow()
  })
})
