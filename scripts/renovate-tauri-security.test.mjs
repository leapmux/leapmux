import { describe, expect, it } from 'bun:test'
import { fixedTauriVersion, tauriAdvisories } from './renovate-tauri-security.mjs'

describe('fixedTauriVersion', () => {
  it('preserves valid fixed versions and rejects malformed alert metadata', () => {
    expect(fixedTauriVersion({})).toBeNull()
    expect(fixedTauriVersion({
      security_vulnerability: {
        first_patched_version: { identifier: '3.0.0' },
      },
    })).toBe('3.0.0')
    for (const identifier of ['', 'not-a-version', 2]) {
      expect(() => fixedTauriVersion({
        security_vulnerability: {
          first_patched_version: { identifier },
        },
      })).toThrow()
    }
  })
})

describe('tauriAdvisories', () => {
  const record = (range, fixed = '2.11.4') => ({
    package: { name: 'tauri', ecosystem: 'rust' },
    vulnerable_version_range: range,
    first_patched_version: fixed === null
      ? null
      : { identifier: fixed },
  })
  const alert = (primary, related = []) => ({
    state: 'open',
    dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
    security_vulnerability: primary,
    security_advisory: { vulnerabilities: related },
  })
  it('keeps applicable minima and all candidate safety ranges independent of alert order', () => {
    const primary = record('>= 2.11.0, < 2.11.4')
    const later = record('>= 2.12.0, < 2.12.2', '2.12.2')
    for (const alerts of [
      [
        alert(primary, [later]),
        alert(record('< 2.11.5', '2.11.5')),
      ],
      [
        alert(record('< 2.11.5', '2.11.5')),
        alert(primary, [later]),
      ],
    ]) {
      const policy = tauriAdvisories(alerts)
      expect(policy.minimum('tauri', '2.11.3')).toBe('2.11.5')
      expect(policy.minimum('tauri', '3.0.0')).toBeNull()
      expect(policy.isSafe('tauri', '2.12.0')).toBe(false)
      expect(policy.isSafe('tauri', '2.12.2')).toBe(true)
      expect(policy.isAffected('tauri', '2.11.3')).toBe(true)
    }
  })
  it('keeps unfixed findings and skips closed, unrelated, and wrong-graph findings', () => {
    const entry = alert(record('< 2.12.0', null))
    const policy = tauriAdvisories([entry])
    expect(policy.minimum('tauri', '2.11.3')).toBeNull()
    expect(policy.isSafe('tauri', '2.11.3')).toBe(false)
    for (const alerts of [
      [],
      [
        { ...entry, state: 'dismissed' },
      ],
      [
        {
          ...entry,
          dependency: { manifest_path: 'other/Cargo.lock' },
        },
      ],
      [
        {
          ...entry,
          security_vulnerability: {
            package: { name: 'unrelated' },
          },
        },
      ],
    ])
      expect(tauriAdvisories(alerts).isAffected('tauri', '2.11.3')).toBe(false)
  })
  it('fails visibly for malformed relevant ranges, versions, and advisory records', () => {
    for (const range of ['', null, 'not a range']) {
      expect(() => tauriAdvisories([
        alert(record(range)),
      ])).toThrow('vulnerable version range')
    }
    expect(() => tauriAdvisories([
      alert(record('< 3', 'invalid')),
    ])).toThrow('fixed version')
    expect(() => tauriAdvisories([
      {
        ...alert(record('< 3')),
        security_advisory: { vulnerabilities: {} },
      },
    ])).toThrow('advisory ranges')
    expect(() => tauriAdvisories(null)).toThrow('array')
  })
})
