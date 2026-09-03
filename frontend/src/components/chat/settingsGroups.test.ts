import type { AvailableOption, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { groupHasOptions, hasOptions, optionSideEffectText } from './settingsGroups'

function group(id: string, optionIds: string[]): AvailableOptionGroup {
  return {
    id,
    label: id,
    order: 0,
    mutable: true,
    defaultValue: optionIds[0] ?? '',
    currentValue: optionIds[0] ?? '',
    options: optionIds.map(o => ({ id: o, name: o })),
  } as unknown as AvailableOptionGroup
}

describe('hasOptions', () => {
  it('accepts a group that offers something to pick', () => {
    expect(hasOptions(group('model', ['opus']))).toBe(true)
  })

  it('rejects a group whose option list has not resolved yet', () => {
    // The worker reports a group before its options arrive. A chip that opens
    // onto nothing is a dead control, and a submenu is a dead end.
    expect(hasOptions(group('model', []))).toBe(false)
  })

  it('rejects an absent group', () => {
    expect(hasOptions(undefined)).toBe(false)
  })
})

describe('groupHasOptions', () => {
  const catalog = [group('model', ['opus', 'sonnet']), group('effort', [])]

  it('finds a populated group by id', () => {
    expect(groupHasOptions(catalog, 'model')).toBe(true)
  })

  it('rejects a group that is present but empty', () => {
    expect(groupHasOptions(catalog, 'effort')).toBe(false)
  })

  it('rejects an id the catalog does not carry', () => {
    expect(groupHasOptions(catalog, 'permissionMode')).toBe(false)
  })

  it('rejects every id when there is no catalog at all', () => {
    // Pre-handshake. Both composer settings surfaces read this, so neither may
    // throw on the undefined catalog.
    expect(groupHasOptions(undefined, 'model')).toBe(false)
  })
})

describe('optionSideEffectText', () => {
  const catalog = [
    { id: 'allow_all', label: 'Allow All', options: [{ id: 'off', name: 'Off' }, { id: 'on', name: 'On' }] },
    { id: 'copilot_assisted_approval', label: 'Assisted Approval', options: [] },
  ] as unknown as AvailableOptionGroup[]
  const option = (clears: { groupId: string, value: string }[]) =>
    ({ id: 'on', name: 'On', clears }) as unknown as AvailableOption

  it('names the group and value a selection also settles, using catalog labels', () => {
    expect(optionSideEffectText(catalog, option([{ groupId: 'allow_all', value: 'off' }])))
      .toBe('Also sets Allow All to Off.')
  })

  it('returns nothing for an option that settles nothing else', () => {
    expect(optionSideEffectText(catalog, option([]))).toBeUndefined()
  })

  it('treats an absent clears field as settling nothing', () => {
    // A catalog object does not always come from a decoded proto -- the tab store and
    // the tests build option literals by hand, and a picker renders whatever it holds.
    expect(optionSideEffectText(catalog, { id: 'on', name: 'On' } as unknown as AvailableOption))
      .toBeUndefined()
  })

  it('skips a side effect whose group the catalog does not carry', () => {
    // A worker can name a group this session never reported; describing it by its wire
    // id would say less than saying nothing.
    expect(optionSideEffectText(catalog, option([{ groupId: 'absent', value: 'off' }]))).toBeUndefined()
  })

  it('falls back to the wire ids when the group is present but the value is not', () => {
    expect(optionSideEffectText(catalog, option([{ groupId: 'allow_all', value: 'unknown' }])))
      .toBe('Also sets Allow All to unknown.')
  })
})
