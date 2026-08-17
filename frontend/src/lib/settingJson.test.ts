import { describe, expect, it } from 'vitest'
import { parseSettingJson } from './settingJson'

describe('parseSettingJson', () => {
  it('returns undefined for empty or malformed JSON', () => {
    expect(parseSettingJson('')).toBeUndefined()
    expect(parseSettingJson('{')).toBeUndefined()
    expect(parseSettingJson('not json')).toBeUndefined()
  })

  it('parses valid JSON including bare scalars', () => {
    expect(parseSettingJson('0')).toBe(0)
    expect(parseSettingJson('{"relay_bytes":0}')).toEqual({ relay_bytes: 0 })
  })

  it('keeps a falsy scalar distinct from an absent value', () => {
    // A stored 0, false, or "" is a real value. Only the empty document
    // means "nothing stored", so the three must not collapse together.
    expect(parseSettingJson('false')).toBe(false)
    expect(parseSettingJson('""')).toBe('')
    expect(parseSettingJson('null')).toBeNull()
  })
})
