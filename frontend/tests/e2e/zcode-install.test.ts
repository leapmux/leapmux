/**
 * Unit tests for the current ZCode installation layouts.
 *
 * A `.test.ts` under `tests/e2e/` runs under vitest, not Playwright: it needs
 * no browser and no hub, so it costs milliseconds here and belongs to
 * `task test-frontend`. Both runner configs are pinned to that rule, and
 * `src/test-support/testFileNaming.test.ts` fails the suite if this file is
 * ever renamed to `.spec.ts`.
 */
import { win32 } from 'node:path'
import { describe, expect, it } from 'vitest'
import { zcodeScriptCandidatePaths } from './zcode-install'

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
