import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { permissionPresetsFor, PROVIDER_PERMISSION_PRESETS } from './permissionPresets'
import { providerFor } from './registry'
import './index'

const providers = Object.values(AgentProvider).filter((value): value is AgentProvider => typeof value === 'number')

describe('PROVIDER_PERMISSION_PRESETS', () => {
  it('lists every proto provider explicitly, including UNSPECIFIED', () => {
    expect(Object.keys(PROVIDER_PERMISSION_PRESETS).map(Number).sort((a, b) => a - b)).toEqual([...providers].sort((a, b) => a - b))
    expect(PROVIDER_PERMISSION_PRESETS[AgentProvider.UNSPECIFIED]).toBeUndefined()
  })

  it.each(providers.filter(provider => provider !== AgentProvider.UNSPECIFIED))('retains the exact production plugin preset object for provider %s', (provider) => {
    const plugin = providerFor(provider)
    expect(plugin, 'Every concrete provider must register its actual browser plugin.').toBeDefined()
    expect(permissionPresetsFor(provider)).toBe(plugin?.controls?.permissionPresets)
  })

  it('retains explicit absence for providers that offer no standard permission preset', () => {
    for (const provider of [AgentProvider.UNSPECIFIED, AgentProvider.OPENCODE, AgentProvider.PI, AgentProvider.CURSOR, AgentProvider.KILO, AgentProvider.JUNIE, AgentProvider.DIRAC, AgentProvider.FAST_AGENT])
      expect(permissionPresetsFor(provider)).toBeUndefined()
    expect(permissionPresetsFor(undefined)).toBeUndefined()
  })

  it('keeps every offered preset change nonempty and uses complete string option values', () => {
    for (const presets of Object.values(PROVIDER_PERMISSION_PRESETS)) {
      if (!presets)
        continue
      for (const preset of Object.values(presets)) {
        const changes = Object.entries(preset.sets)
        expect(changes.length).toBeGreaterThan(0)
        for (const [groupId, value] of changes) {
          expect(groupId.trim()).not.toBe('')
          expect(typeof value).toBe('string')
          if (typeof value !== 'string')
            throw new Error('The provider permission value must be a string.')
          expect(value.trim()).not.toBe('')
        }
      }
    }
  })
})
