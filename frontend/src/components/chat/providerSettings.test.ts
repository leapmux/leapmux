import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { activePermissionPreset, permissionPresetActive } from './providerSettings'

/**
 * A mutable option group fixture. `permissionPresetActive` compares against
 * `resolvedCurrent`, which reads the optimistic value first and the catalog's
 * `currentValue` second.
 */
function group(id: string, optionIds: string[], currentValue: string): AvailableOptionGroup {
  return {
    id,
    label: id,
    options: optionIds.map(optionId => ({ id: optionId })),
    defaultValue: optionIds[0],
    currentValue,
    mutable: true,
  } as unknown as AvailableOptionGroup
}

const MODES = ['default', 'auto', 'bypassPermissions']

describe('permissionPresetActive', () => {
  const groups = [group('permissionMode', MODES, 'auto')]

  it('reports a preset whose every axis already holds its value', () => {
    expect(permissionPresetActive({ sets: { permissionMode: 'auto' } }, groups, {})).toBe(true)
  })

  it('reports nothing for a preset that would change an axis', () => {
    expect(permissionPresetActive({ sets: { permissionMode: 'bypassPermissions' } }, groups, {})).toBe(false)
  })

  it('prefers the optimistic value over the catalog value', () => {
    // A switch the user just made, before the agent confirms it. The pill must
    // open on where the session is going, not where it was.
    const values = { permissionMode: 'bypassPermissions' }
    expect(permissionPresetActive({ sets: { permissionMode: 'bypassPermissions' } }, groups, values)).toBe(true)
    expect(permissionPresetActive({ sets: { permissionMode: 'auto' } }, groups, values)).toBe(false)
  })

  it('requires EVERY axis of a multi-axis preset', () => {
    // Codex switches approval, network and sandbox together. Two of three
    // matching is not the preset.
    const codex = [
      group('permissionMode', ['on-request', 'never'], 'never'),
      group('network_access', ['disabled', 'enabled'], 'enabled'),
      group('sandbox_policy', ['read-only', 'danger-full-access'], 'read-only'),
    ]
    const preset = {
      sets: { permissionMode: 'never', network_access: 'enabled', sandbox_policy: 'danger-full-access' },
    }

    expect(permissionPresetActive(preset, codex, {})).toBe(false)
    expect(permissionPresetActive(preset, codex, { sandbox_policy: 'danger-full-access' })).toBe(true)
  })

  it('reports nothing for an absent preset or one that sets nothing', () => {
    // An empty change writes no axis, so "already applied" would be vacuously
    // true and would open the group on a preset that does nothing.
    expect(permissionPresetActive(undefined, groups, {})).toBe(false)
    expect(permissionPresetActive({ sets: {} }, groups, {})).toBe(false)
  })
})

describe('activePermissionPreset', () => {
  const presets = {
    smart: { sets: { permissionMode: 'auto' } },
    bypass: { sets: { permissionMode: 'bypassPermissions' } },
  }

  it('names the preset the session has on', () => {
    expect(activePermissionPreset(presets, [group('permissionMode', MODES, 'auto')], {})).toBe('smart')
    expect(activePermissionPreset(presets, [group('permissionMode', MODES, 'bypassPermissions')], {})).toBe('bypass')
  })

  it('names nothing when neither preset is on', () => {
    expect(activePermissionPreset(presets, [group('permissionMode', MODES, 'default')], {})).toBeUndefined()
    expect(activePermissionPreset({}, [group('permissionMode', MODES, 'auto')], {})).toBeUndefined()
  })

  it('prefers smart when both report active', () => {
    // Copilot's two presets switch DIFFERENT axes, so both can be on at once.
    const copilot = {
      smart: { sets: { copilot_assisted_approval: 'on' } },
      bypass: { sets: { allow_all: 'on' } },
    }
    const groups = [
      group('copilot_assisted_approval', ['off', 'on'], 'on'),
      group('allow_all', ['off', 'on'], 'on'),
    ]

    expect(activePermissionPreset(copilot, groups, {})).toBe('smart')
  })

  it('names nothing without a catalog', () => {
    expect(activePermissionPreset(presets, undefined, undefined)).toBeUndefined()
  })
})
