/**
 * Unit tests for the ZCode E2E skip probe.
 *
 * A `.test.ts` under `tests/e2e/` runs under vitest, not Playwright: it needs
 * no browser and no hub, so it costs milliseconds here and belongs to
 * `task test-frontend`. Both runner configs are pinned to that rule, and
 * `src/test-support/testFileNaming.test.ts` fails the suite if this file is
 * ever renamed to `.spec.ts`.
 */
import { win32 } from 'node:path'
import { describe, expect, it } from 'vitest'
import { computeZCodeE2ESkipReason, zcodeScriptCandidatePaths } from './zcode-install'

const SKIP_MESSAGE = 'ZCode E2E requires a zcode launcher on PATH or a ZCode.app / ZCode install that carries zcode.cjs'

describe('zcodeScriptCandidatePaths', () => {
  it('lists the per-user bundle before the machine-wide one on darwin', () => {
    expect(zcodeScriptCandidatePaths('darwin', '/Users/ada', {})).toEqual([
      '/Users/ada/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs',
      '/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs',
    ])
  })

  it('lists the per-user share before the system paths on linux', () => {
    expect(zcodeScriptCandidatePaths('linux', '/home/ada', {})).toEqual([
      '/home/ada/.local/share/ZCode/resources/glm/zcode.cjs',
      '/opt/ZCode/resources/glm/zcode.cjs',
      '/usr/share/zcode/resources/glm/zcode.cjs',
      '/usr/lib/zcode/resources/glm/zcode.cjs',
    ])
  })

  it('omits a Windows root whose env var is empty', () => {
    expect(zcodeScriptCandidatePaths('win32', 'C:\\Users\\ada', {
      ProgramFiles: 'C:\\Program Files',
    })).toEqual([
      win32.join('C:\\Program Files', 'ZCode', 'resources', 'glm', 'zcode.cjs'),
    ])
  })

  it('prefers LOCALAPPDATA over Program Files on win32', () => {
    const paths = zcodeScriptCandidatePaths('win32', 'C:\\Users\\ada', {
      'LOCALAPPDATA': 'C:\\Users\\ada\\AppData\\Local',
      'ProgramFiles': 'C:\\Program Files',
      'ProgramFiles(x86)': 'C:\\Program Files (x86)',
    })
    expect(paths[0]).toBe(win32.join('C:\\Users\\ada\\AppData\\Local', 'Programs', 'ZCode', 'resources', 'glm', 'zcode.cjs'))
    expect(paths).toHaveLength(3)
  })
})

describe('computeZCodeE2ESkipReason', () => {
  const USABLE_CONFIG = JSON.stringify({
    provider: { 'builtin:zai': { options: { apiKey: 'k' }, models: { 'GLM-5.3': {} } } },
  })
  const CONFIG_PATH = '/Users/ada/.zcode/v2/config.json'
  const base = {
    platform: 'darwin' as const,
    home: '/Users/ada',
    env: {},
    scriptExists: () => false,
    // Signed in, so the install half of the question is what each case below varies.
    readConfig: (path: string) => (path === CONFIG_PATH ? USABLE_CONFIG : null),
  }

  it('skips when an override points at a missing file', () => {
    expect(computeZCodeE2ESkipReason({
      ...base,
      scriptOverride: '/tmp/missing.cjs',
      launcherOnPath: true,
    })).toBe('LEAPMUX_ZCODE_SCRIPT points at a missing file: /tmp/missing.cjs')
  })

  it('runs when the override exists, even with no PATH launcher', () => {
    expect(computeZCodeE2ESkipReason({
      ...base,
      scriptOverride: '/tmp/zcode.cjs',
      scriptExists: path => path === '/tmp/zcode.cjs',
      launcherOnPath: false,
    })).toBeNull()
  })

  it('runs when a launcher is on PATH', () => {
    expect(computeZCodeE2ESkipReason({
      ...base,
      scriptOverride: undefined,
      launcherOnPath: true,
    })).toBeNull()
  })

  it('runs when a bundled script exists', () => {
    expect(computeZCodeE2ESkipReason({
      ...base,
      scriptOverride: undefined,
      launcherOnPath: false,
      scriptExists: path => path === '/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs',
    })).toBeNull()
  })

  it('skips when nothing is installed', () => {
    expect(computeZCodeE2ESkipReason({
      ...base,
      scriptOverride: undefined,
      launcherOnPath: false,
    })).toBe(SKIP_MESSAGE)
  })

  it('does not treat an empty override as a specified script', () => {
    expect(computeZCodeE2ESkipReason({
      ...base,
      scriptOverride: '',
      launcherOnPath: true,
    })).toBeNull()
  })

  // StartZCode reads the configuration too, and fails on it. A machine that has the
  // desktop application but was never signed in must SKIP, or every spec fails at
  // sendMessage with a timeout that names nothing.
  describe('the configuration half', () => {
    const installed = { ...base, scriptOverride: undefined, launcherOnPath: true }

    it('skips when the configuration is absent', () => {
      expect(computeZCodeE2ESkipReason({ ...installed, readConfig: () => null }))
        .toContain(CONFIG_PATH)
    })

    it('skips when the configuration is not json', () => {
      expect(computeZCodeE2ESkipReason({ ...installed, readConfig: () => '{' }))
        .toContain('sign in with the ZCode application once')
    })

    it.each([
      ['no provider at all', '{}'],
      ['an empty provider map', '{"provider":{}}'],
      ['a provider with no key', '{"provider":{"p":{"options":{},"models":{"m":{}}}}}'],
      ['a provider whose key is blank', '{"provider":{"p":{"options":{"apiKey":"  "},"models":{"m":{}}}}}'],
      ['a provider with no model', '{"provider":{"p":{"options":{"apiKey":"k"},"models":{}}}}'],
      ['a provider list that is an array', '{"provider":[]}'],
      ['a document that is not an object', '"nope"'],
    ])('skips for %s', (_name, text) => {
      expect(computeZCodeE2ESkipReason({ ...installed, readConfig: () => text }))
        .toContain('carry a model provider with an API key')
    })

    it('runs when one provider of several is usable', () => {
      const text = JSON.stringify({
        provider: {
          keyless: { options: {}, models: { m: {} } },
          usable: { options: { apiKey: 'k' }, models: { m: {} } },
        },
      })
      expect(computeZCodeE2ESkipReason({ ...installed, readConfig: () => text })).toBeNull()
    })

    // The install half is checked FIRST: a machine with neither must report the
    // install, which is the step the user has to take before signing in.
    it('reports the missing install rather than the missing configuration', () => {
      expect(computeZCodeE2ESkipReason({
        ...base,
        scriptOverride: undefined,
        launcherOnPath: false,
        readConfig: () => null,
      })).toBe(SKIP_MESSAGE)
    })
  })
})
