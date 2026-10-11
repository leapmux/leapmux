import type { StartupOptionGroup } from './capabilities'
import { describe, expect, it } from 'vitest'
import { resolveStartupOptionValues, validateStartupOptionGroups } from './startupOptions'

function groups(): StartupOptionGroup[] {
  return [{
    id: 'trust',
    label: 'Trust',
    defaultValue: 'native',
    readOnlyReason: 'Choose before creation',
    options: [{ value: 'native', label: 'Native' }, { value: 'agent', label: 'Agent' }],
  }]
}

describe('validateStartupOptionGroups', () => {
  it.each([
    ['null group', 'The provider supplies an invalid or repeated startup option group.', (source: StartupOptionGroup[]) => { source[0] = null as unknown as StartupOptionGroup }],
    ['blank ID', 'The provider supplies an invalid or repeated startup option group.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, id: ' ' } }],
    ['blank value', 'The provider supplies an invalid or repeated startup option.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, options: [{ value: ' ', label: 'Blank' }], defaultValue: ' ' } }],
    ['null option', 'The provider supplies an invalid or repeated startup option.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, options: [null as unknown as StartupOptionGroup['options'][number]] } }],
    ['absent options', 'The provider supplies an invalid or repeated startup option group.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, options: undefined as unknown as StartupOptionGroup['options'] } }],
    ['repeated ID', 'The provider supplies an invalid or repeated startup option group.', (source: StartupOptionGroup[]) => { source.push(source[0]!) }],
    ['repeated value', 'The provider supplies an invalid or repeated startup option.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, options: [source[0]!.options[0]!, source[0]!.options[0]!] } }],
    ['empty options', 'The provider startup default is outside its options.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, options: [] } }],
    ['foreign default', 'The provider startup default is outside its options.', (source: StartupOptionGroup[]) => { source[0] = { ...source[0]!, defaultValue: 'other' } }],
  ])('rejects %s as %s', (_case, reason, change) => {
    const source = groups()
    change(source)
    expect(() => validateStartupOptionGroups(source)).toThrow(reason)
  })
})

describe('resolveStartupOptionValues', () => {
  it('fills defaults and preserves its inputs', () => {
    const source = groups()
    const selected = { trust: 'agent' }
    const before = structuredClone(source)
    expect(resolveStartupOptionValues(source, {})).toEqual({ trust: 'native' })
    expect(resolveStartupOptionValues(source, selected)).toEqual(selected)
    expect(source).toEqual(before)
    expect(selected).toEqual({ trust: 'agent' })
  })

  it.each([
    [{ trust: 'other' }, 'The selected startup value is outside the provider options.'],
    [{ foreign: 'agent' }, 'The startup selection belongs to another provider.'],
    [{ trust: '' }, 'The selected startup value is outside the provider options.'],
  ])('rejects %j as %s', (selected, reason) => {
    expect(() => resolveStartupOptionValues(groups(), selected)).toThrow(reason)
  })

  it('accepts no groups and no selections', () => {
    expect(resolveStartupOptionValues([], {})).toEqual({})
  })

  it('retains an option ID that overlaps an object prototype property', () => {
    const source = groups().map(group => ({ ...group, id: '__proto__' }))
    const result = resolveStartupOptionValues(source, {})
    expect(Object.hasOwn(result, '__proto__')).toBe(true)
    expect(Object.getOwnPropertyDescriptor(result, '__proto__')?.value).toBe('native')
  })
})
