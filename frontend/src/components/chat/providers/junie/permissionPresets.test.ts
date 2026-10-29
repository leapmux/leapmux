import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { activePermissionPreset, usablePresets } from '../../providerSettings'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { juniePermissionPresets } from './permissionPresets'

describe('juniePermissionPresets', () => {
  it('selects Brave Auto for smart and Brave On for bypass', () => {
    // The literals pin Junie's wire vocabulary, so a typo in JUNIE_BRAVE cannot pass.
    expect(juniePermissionPresets.smart?.sets).toEqual({ brave_mode: 'brave-auto' })
    expect(juniePermissionPresets.bypass?.sets).toEqual({ brave_mode: 'on' })
  })

  it('never touches the permission-mode axis', () => {
    // Brave mode and the session mode are independent axes, so Plan and a
    // preset stay on together.
    expect(Object.keys(juniePermissionPresets.smart?.sets ?? {})).not.toContain(OPTION_ID_PERMISSION_MODE)
    expect(Object.keys(juniePermissionPresets.bypass?.sets ?? {})).not.toContain(OPTION_ID_PERMISSION_MODE)
  })

  it.each([
    ['absent', undefined],
    ['empty', []],
    [
      'without values',
      [
        create(AvailableOptionGroupSchema, { id: 'brave_mode', mutable: true }),
      ],
    ],
    [
      'immutable',
      [
        create(AvailableOptionGroupSchema, {
          id: 'brave_mode',
          mutable: false,
          options: [
            { id: 'brave-auto' },
            { id: 'on' },
          ],
        }),
      ],
    ],
    [
      'unrelated',
      [
        create(AvailableOptionGroupSchema, {
          id: 'permissionMode',
          mutable: true,
          options: [
            { id: 'brave-auto' },
            { id: 'on' },
          ],
        }),
      ],
    ],
    [
      'unknown values',
      [
        create(AvailableOptionGroupSchema, {
          id: 'brave_mode',
          mutable: true,
          options: [
            { id: '' },
            { id: 'unknown' },
          ],
        }),
      ],
    ],
  ])('offers no shortcuts when the catalog is %s', (_, groups) => {
    expect(usablePresets(juniePermissionPresets, groups)).toEqual({})
  })

  it.each([
    ['brave-auto', 'smart'],
    ['on', 'bypass'],
  ])('offers only the preset whose value is %s', (value, kind) => {
    const groups = [
      create(AvailableOptionGroupSchema, {
        id: 'brave_mode',
        mutable: true,
        options: [
          { id: value },
        ],
      }),
    ]

    expect(Object.keys(usablePresets(juniePermissionPresets, groups))).toEqual([kind])
  })

  it.each([
    ['brave-auto', 'smart'],
    ['on', 'bypass'],
    ['off', undefined],
    ['', undefined],
    ['unknown', undefined],
  ])('derives the active preset from Brave value %s while Plan stays active', (value, expected) => {
    const groups = [
      create(AvailableOptionGroupSchema, { id: 'brave_mode', currentValue: value }),
      create(AvailableOptionGroupSchema, { id: 'permissionMode', currentValue: 'plan' }),
    ]

    expect(activePermissionPreset(juniePermissionPresets, groups, {})).toBe(expected)
    expect(groups[1]?.currentValue).toBe('plan')
  })
})
