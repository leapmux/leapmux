import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'
import { refusedModifierKeys } from '~/test-support/platformModifierKeys'
import { posixRelative } from '~/test-support/sourceTree'

// E2E guard: a key chord with the platform modifier spells it `PLATFORM_MOD` (`tests/e2e/helpers/ui.ts`), never a
// fixed `Meta` and never Playwright's `ControlOrMeta`. A fixed `Meta` is a different chord on Linux and Windows, and
// CI runs no E2E, so no run on those platforms reports it. `platformModifierKeys.ts` holds the analysis.

/** The key of a site: its path below the E2E root and its enclosing function, as `fixedWaits.test.ts` keys a site. */
function siteKey(path: string, enclosingFunction = ''): string {
  return `${path} ${enclosingFunction}`
}

/**
 * The sites that still press a fixed `Meta`, by file and enclosing function. Each one is a defect on Linux and
 * Windows: press `PLATFORM_MOD` there, and delete the entry. An entry that matches no site fails the guard, so a
 * converted site takes its entry with it.
 */
const UNCONVERTED: ReadonlySet<string> = new Set()

/** Analyze sources keyed by a path below a fixed root. */
function analyze(files: Record<string, string>) {
  return refusedModifierKeys(Object.entries(files).map(([path, source]) => ({ path: `/e2e/${path}`, source })))
}

describe('refusedModifierKeys', () => {
  it('finds a fixed Meta chord of a keyboard press, with its line and its enclosing function', () => {
    const findings = analyze({
      'helpers/send.ts': `export async function send(page) {
  await page.keyboard.press('Meta+Enter')
}`,
    })
    expect(findings).toEqual([{ path: '/e2e/helpers/send.ts', line: 2, enclosingFunction: 'send', key: 'Meta' }])
  })

  it('finds the refused names in every key method, in a modifiers array, and inside a longer chord', () => {
    const findings = analyze({
      'spec.ts': `await locator.press("ControlOrMeta+k")
await page.keyboard.down('MetaLeft')
await page.keyboard.up(\`MetaRight\`)
await locator.click({ modifiers: ['Shift', 'Meta'] })
await page.keyboard.press('Shift+Meta+ArrowLeft')
await page.press('#editor', 'Meta+a')`,
    })
    expect(findings.map(finding => [finding.line, finding.key])).toEqual([
      [1, 'ControlOrMeta'],
      [2, 'MetaLeft'],
      [3, 'MetaRight'],
      [4, 'Meta'],
      [5, 'Meta'],
      [6, 'Meta'],
    ])
  })

  it('finds a refused name in the static text of a template, but not a modifier that the template computes', () => {
    const findings = analyze({
      'spec.ts': `await page.keyboard.press(\`Meta+\${key}\`)
await page.keyboard.press(\`\${PLATFORM_MOD}+Enter\`)
await page.keyboard.press(\`\${PLATFORM_MOD}+\${key}\`)`,
    })
    expect(findings.map(finding => finding.line)).toEqual([1])
  })

  it('gives an empty enclosing function to a press in a test callback', () => {
    const findings = analyze({ 'spec.ts': 'test(\'case\', async ({ page }) => { await page.keyboard.press(\'Meta+a\') })' })
    expect(findings).toEqual([{ path: '/e2e/spec.ts', line: 1, enclosingFunction: '', key: 'Meta' }])
  })

  it('ignores a fixed Control, a key that only contains a refused name, and a name outside a key argument', () => {
    const findings = analyze({
      'spec.ts': `await page.keyboard.press('Control+D')
await page.keyboard.press('Control+Backquote')
await page.keyboard.press('KeyM')
await page.keyboard.press('Shift+Metadata')
await page.keyboard.type('Meta+Enter')
await page.keyboard.insertText('Meta+Enter')
await page.keyboard.press(chord)
const mod = platform === 'darwin' ? 'Meta' : 'Control'
const options = { modifiers: modifierKeys }
// page.keyboard.press('Meta+Enter') presses a fixed Meta.`,
    })
    expect(findings).toEqual([])
  })
})

describe('e2e platform modifier keys', () => {
  const files = collectE2EFiles().map(path => ({ path, source: readFileSync(path, 'utf-8') }))
  const findings = refusedModifierKeys(files)
  const key = (finding: { path: string, enclosingFunction: string }) => siteKey(posixRelative(e2eRoot, finding.path), finding.enclosingFunction)

  it('scans the e2e tree', () => {
    expect(files.length).toBeGreaterThan(100)
  })

  it('presses PLATFORM_MOD for the platform modifier', () => {
    const offenders = findings
      .filter(finding => !UNCONVERTED.has(key(finding)))
      .map(finding => `${posixRelative(e2eRoot, finding.path)}:${finding.line}  ${finding.key} in ${finding.enclosingFunction || 'a callback'}`)
    const hint = [
      'A fixed Meta is a different chord on Linux and Windows, and ControlOrMeta is a second spelling.',
      'Join PLATFORM_MOD of tests/e2e/helpers/ui.ts to the key in a template literal:',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })

  it('keeps no unconverted site that the e2e tree no longer holds', () => {
    const found = new Set(findings.map(key))
    expect([...UNCONVERTED].filter(entry => !found.has(entry))).toEqual([])
  })
})
