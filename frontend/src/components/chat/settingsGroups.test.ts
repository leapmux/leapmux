import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { groupHasOptions, hasOptions } from './settingsGroups'

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
