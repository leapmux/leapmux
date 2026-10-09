import { describe, expect, test } from 'bun:test'
import { checkStartupOptionGroups, emitGoProviderProtocol, emitTsProviderProtocol } from './generate-contracts.mjs'

const spec = {
  name: 'fixture-protocol', goPrefix: 'Fixture', tsPrefix: 'FIXTURE', title: 'Fixture',
  tables: [
    { key: 'optionIds', goTable: 'OptionID', tsTable: 'OPTION_ID', tsType: 'FixtureOptionID', doc: 'option IDs' },
    { key: 'values', goTable: 'Value', tsTable: 'VALUE', tsType: 'FixtureValue', doc: 'option values' },
  ],
}

function contract() {
  return {
    optionIds: { Mode: 'mode', Alternate: 'alternate' },
    values: { Native: 'native', Explicit: 'explicit' },
    startupOptionGroups: {
      Mode: {
        id: 'Mode', valuesTable: 'values', label: 'Mode', defaultValue: 'Native', readOnlyReason: 'Choose before launch',
        options: [{ value: 'Native', label: 'Native' }, { value: 'Explicit', label: 'Explicit', description: 'Choose explicitly' }],
      },
    },
  }
}

describe('checkStartupOptionGroups', () => {
  test('accepts a complete axis and leaves the source unchanged', () => {
    const source = contract()
    const before = structuredClone(source)
    expect(() => checkStartupOptionGroups(spec, source)).not.toThrow()
    expect(source).toEqual(before)
  })

  test.each([
    ['duplicate descriptor ID', source => { source.startupOptionGroups.Duplicate = structuredClone(source.startupOptionGroups.Mode) }],
    ['duplicate option value', source => { source.startupOptionGroups.Mode.options.push({ value: 'Native', label: 'Again' }) }],
    ['unknown ID key', source => { source.startupOptionGroups.Mode.id = 'Missing' }],
    ['unknown values table', source => { source.startupOptionGroups.Mode.valuesTable = 'missing' }],
    ['empty label', source => { source.startupOptionGroups.Mode.label = ' ' }],
    ['empty read-only reason', source => { source.startupOptionGroups.Mode.readOnlyReason = '' }],
    ['empty options', source => { source.startupOptionGroups.Mode.options = [] }],
    ['unknown value', source => { source.startupOptionGroups.Mode.options[0].value = 'Missing' }],
    ['default outside options', source => { source.startupOptionGroups.Mode.defaultValue = 'Missing' }],
    ['null option', source => { source.startupOptionGroups.Mode.options[0] = null }],
    ['blank resolved ID', source => { source.optionIds.Mode = ' ' }],
    ['blank resolved value', source => { source.values.Native = ' ' }],
    ['malformed description', source => { source.startupOptionGroups.Mode.options[0].description = 7 }],
    ['inherited ID key', source => { source.startupOptionGroups.Mode.id = '__proto__' }],
  ])('rejects %s', (_case, change) => {
    const source = contract()
    change(source)
    expect(() => checkStartupOptionGroups(spec, source)).toThrow()
  })
})

test('rejects an undeclared option ID table', () => {
  const incomplete = { ...spec, tables: spec.tables.filter(table => table.key !== 'optionIds') }
  expect(() => checkStartupOptionGroups(incomplete, contract())).toThrow()
})

describe('startup option-group emission', () => {
  test('derives both defaults and ordered options from the shared axis', () => {
    const source = contract()
    source.startupOptionGroups.Mode.defaultValue = 'Explicit'
    source.startupOptionGroups.Mode.options.reverse()
    const go = emitGoProviderProtocol(spec, source)
    const ts = emitTsProviderProtocol(spec, source)
    expect(go).toContain('DefaultValue: FixtureValueExplicit')
    expect(ts).toContain('defaultValue: FIXTURE_VALUE.Explicit')
    expect(go.indexOf('Value: FixtureValueExplicit')).toBeLessThan(go.indexOf('Value: FixtureValueNative'))
    expect(ts.indexOf('value: FIXTURE_VALUE.Explicit')).toBeLessThan(ts.indexOf('value: FIXTURE_VALUE.Native'))
    expect(go).toContain('Description: "Choose explicitly"')
    expect(ts).toContain('description: "Choose explicitly"')
  })
})
