import { describe, expect, it } from 'vitest'
import { computeKimiE2ESkipReason } from './kimi-install'

describe('computeKimiE2ESkipReason', () => {
  it('runs against Kimi Code 2.0 or later, a pre-release included', () => {
    expect(computeKimiE2ESkipReason('2.0.2\n')).toBeNull()
    expect(computeKimiE2ESkipReason('2.1.0-beta.1')).toBeNull()
    expect(computeKimiE2ESkipReason('10.0.0')).toBeNull()
  })

  it('runs against the earliest release that the provider speaks', () => {
    expect(computeKimiE2ESkipReason('2.0.0')).toBeNull()
  })

  it('reads a release number that follows a name or a prefix', () => {
    expect(computeKimiE2ESkipReason('kimi-code 2.0.2')).toBeNull()
    expect(computeKimiE2ESkipReason('v2.0.2')).toBeNull()
  })

  it('skips when no kimi runs', () => {
    expect(computeKimiE2ESkipReason(null)).toBe('Kimi Code E2E requires a kimi CLI on PATH')
  })

  it('skips the legacy Python kimi-cli, which prints its name before the number', () => {
    expect(computeKimiE2ESkipReason('kimi, version 1.5.0'))
      .toBe('Kimi Code E2E requires Kimi Code 2.0 or later, and the kimi on PATH is 1.5.0, the legacy Python kimi-cli')
  })

  it('skips every release before the earliest one', () => {
    expect(computeKimiE2ESkipReason('1.99.99')).toContain('the kimi on PATH is 1.99.99')
    expect(computeKimiE2ESkipReason('0.0.0')).toContain('the kimi on PATH is 0.0.0')
  })

  // The worker's `parseKimiVersion` reads the FIRST release number too, so the skip and
  // the start agree on the same output.
  it('reads the first release number when the output states several', () => {
    expect(computeKimiE2ESkipReason('kimi, version 1.5.0 (Python 3.12.1)')).toContain('the kimi on PATH is 1.5.0')
    expect(computeKimiE2ESkipReason('Kimi Code 2.0.2 (node 22.11.0)')).toBeNull()
  })

  it('skips an answer that states no release number', () => {
    const reason = 'Kimi Code E2E requires Kimi Code 2.0 or later, and `kimi --version` states no release number'
    expect(computeKimiE2ESkipReason('')).toBe(reason)
    expect(computeKimiE2ESkipReason('kimi dev build')).toBe(reason)
    expect(computeKimiE2ESkipReason('2.0')).toBe(reason)
  })
})
