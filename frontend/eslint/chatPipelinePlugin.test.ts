import type { ProviderFrameKind } from '../src/generated/contracts/provider-frame-kinds'
import { describe, expect, it } from 'vitest'
import plugin, { rulesVersion } from './chatPipelinePlugin'

const MODULES = [['chatPipelinePlugin.ts', 'rule code'], ['providerWireTokens.ts', 'token code']] as const
const KINDS: readonly ProviderFrameKind[] = [{ literal: 'item.started', match: 'name', source: 'test-protocol events' }]

// ESLint writes a plugin into its cache key as `meta.name@meta.version`. The version
// must change with each input of the rules, or `eslint --cache` keeps a stale result.
describe('rulesVersion', () => {
  it('gives the same version for the same rules and tokens', () => {
    expect(rulesVersion(MODULES, KINDS)).toBe(rulesVersion(MODULES, [...KINDS]))
  })

  it('changes when a contract gains a frame kind', () => {
    const more = [...KINDS, { literal: 'item.completed', match: 'name', source: 'test-protocol events' } as const]
    expect(rulesVersion(MODULES, more)).not.toBe(rulesVersion(MODULES, KINDS))
  })

  it('changes when a frame kind moves to another table', () => {
    const moved = [{ ...KINDS[0]!, source: 'other-protocol events' }]
    expect(rulesVersion(MODULES, moved)).not.toBe(rulesVersion(MODULES, KINDS))
  })

  it('changes when a rule module changes', () => {
    const edited = [MODULES[0], ['providerWireTokens.ts', 'token code, edited']] as const
    expect(rulesVersion(edited, KINDS)).not.toBe(rulesVersion(MODULES, KINDS))
  })

  // The separator keeps the boundary between a name and its source, so text that
  // moves from one to the other is still a change.
  it('changes when text moves between a module name and its source', () => {
    expect(rulesVersion([['ab', 'c']], KINDS)).not.toBe(rulesVersion([['a', 'bc']], KINDS))
  })
})

describe('chatPipelinePlugin', () => {
  it('states a name and a version, so the eslint cache key includes the rules', () => {
    expect(plugin.meta?.name).toBe('chat-pipeline')
    expect(plugin.meta?.version).toMatch(/^[0-9a-f]{16}$/)
  })
})
