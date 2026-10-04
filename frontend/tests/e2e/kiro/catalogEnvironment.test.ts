import { describe, expect, it } from 'vitest'
import { kiroCatalogEnvironment } from './catalogEnvironment'

describe('kiroCatalogEnvironment', () => {
  it('preserves the isolated profile and endpoints while disabling the native deferred experiment', () => {
    const environment = Object.freeze({ KIRO_HOME: '/private/kiro', KIRO_API_KEY: 'mock-only', KIRO_REMOTE_SESSIONS_ENDPOINT: 'http://127.0.0.1:1234', KIRO_FEATURE_TOOL_LOAD_ENABLED: 'true', EMPTY_VALUE: '' })
    const result = kiroCatalogEnvironment(environment)
    expect(result).toEqual({ ...environment, KIRO_FEATURE_TOOL_LOAD_ENABLED: 'false' })
    expect(result).not.toBe(environment)
    expect(environment.KIRO_FEATURE_TOOL_LOAD_ENABLED).toBe('true')
  })

  it('preserves an absent native feature override and unrelated empty values', () => {
    expect(kiroCatalogEnvironment({ KIRO_HOME: '/private/kiro', EMPTY_VALUE: '' })).toEqual({ KIRO_HOME: '/private/kiro', EMPTY_VALUE: '', KIRO_FEATURE_TOOL_LOAD_ENABLED: 'false' })
  })

  it.each([undefined, null, [], '', 0, false, {}, { KIRO_HOME: '' }, { KIRO_HOME: '   ' }, { KIRO_HOME: 0 }])('rejects a malformed profile environment %j', (environment) => {
    expect(() => kiroCatalogEnvironment(environment)).toThrow('isolated profile environment')
  })

  it.each([0, false, null, undefined, {}])('rejects a non-string native environment field %j', (value) => {
    expect(() => kiroCatalogEnvironment({ KIRO_HOME: '/private/kiro', VALUE: value })).toThrow('non-string value')
  })
})
