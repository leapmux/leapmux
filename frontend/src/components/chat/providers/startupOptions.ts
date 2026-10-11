import type { StartupOptionGroup } from './capabilities'
import { isObject } from '~/lib/jsonPick'

function nonemptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim() !== ''
}

/** Validate startup metadata without changing the provider's descriptors. */
export function validateStartupOptionGroups(groups: readonly StartupOptionGroup[]): void {
  if (!Array.isArray(groups))
    throw new Error('The provider startup groups must be an array.')
  const ids = new Set<string>()
  for (const group of groups) {
    if (!isObject(group) || !nonemptyString(group.id) || !nonemptyString(group.label)
      || !nonemptyString(group.readOnlyReason) || ids.has(group.id) || !Array.isArray(group.options)) {
      throw new Error('The provider supplies an invalid or repeated startup option group.')
    }
    ids.add(group.id)
    const values = new Set<string>()
    for (const option of group.options) {
      if (!isObject(option) || !nonemptyString(option.value) || !nonemptyString(option.label)
        || values.has(option.value) || (option.description !== undefined && !nonemptyString(option.description))) {
        throw new Error('The provider supplies an invalid or repeated startup option.')
      }
      values.add(option.value)
    }
    if (!nonemptyString(group.defaultValue) || !values.has(group.defaultValue))
      throw new Error('The provider startup default is outside its options.')
  }
}

/** Fill omitted choices from the shared default and reject foreign selections. */
export function resolveStartupOptionValues(groups: readonly StartupOptionGroup[], selected: Readonly<Record<string, string>>): Record<string, string> {
  validateStartupOptionGroups(groups)
  const entries: [string, string][] = []
  for (const group of groups) {
    const value = Object.hasOwn(selected, group.id) ? selected[group.id] : group.defaultValue
    if (!group.options.some(option => option.value === value))
      throw new Error('The selected startup value is outside the provider options.')
    entries.push([group.id, value!])
  }
  const values: Record<string, string> = Object.fromEntries(entries)
  if (Object.keys(selected).some(id => !Object.hasOwn(values, id)))
    throw new Error('The startup selection belongs to another provider.')
  return values
}
