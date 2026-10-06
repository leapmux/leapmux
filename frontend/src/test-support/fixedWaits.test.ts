import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'
import { fixedWaits } from '~/test-support/fixedWaits'
import { posixRelative } from '~/test-support/sourceTree'

// E2E guard: no spec or helper may size a window with `waitForTimeout`. A fixed wait ends on schedule however late the
// state change that it covers comes, so it fails under load and wastes its full length when nothing happens. Wait for
// the injected end of the transient state, or for a proof that cannot pass early. `fixedWaits.ts` holds the analysis.

/**
 * The fixed waits that the guard accepts, by file and enclosing function, each with its reason. An entry that matches
 * no wait fails the guard, so a removed wait takes its entry with it.
 */
const ACCEPTED: ReadonlyMap<string, string> = new Map([
  [
    'helpers/touch.ts touchHold',
    'The time is the gesture, not a wait for a state change: a long press keeps the finger down for `holdMs`, and the '
    + 'page times the press with its own timer.',
  ],
])

/** Analyze sources keyed by a path below a fixed root. */
function analyze(files: Record<string, string>) {
  return fixedWaits(Object.entries(files).map(([path, source]) => ({ path: `/e2e/${path}`, source })))
}

describe('fixedWaits', () => {
  it('finds a call on a page, with its line and its enclosing function', () => {
    const findings = analyze({
      'helpers/hold.ts': `export async function hold(page) {
  await page.waitForTimeout(500)
}`,
    })
    expect(findings).toEqual([{ path: '/e2e/helpers/hold.ts', line: 2, enclosingFunction: 'hold' }])
  })

  it('finds a call on any receiver, with optional chaining, and through a string key', () => {
    const findings = analyze({
      'spec.ts': `await frame.waitForTimeout(1)
await this.page?.waitForTimeout(2)
await page['waitForTimeout'](3)
await page[\`waitForTimeout\`](4)`,
    })
    expect(findings.map(finding => finding.line)).toEqual([1, 2, 3, 4])
  })

  it('finds a reference that is not a call: a bound method and a destructured one', () => {
    const findings = analyze({
      'spec.ts': `const wait = page.waitForTimeout.bind(page)
const { waitForTimeout } = page
const { waitForTimeout: pause } = page`,
    })
    expect(findings.map(finding => finding.line)).toEqual([1, 2, 3])
  })

  it('reports the line of the method name in a call that spans lines', () => {
    const findings = analyze({ 'spec.ts': 'await page\n  .waitForTimeout(1)' })
    expect(findings.map(finding => finding.line)).toEqual([2])
  })

  it('gives an empty enclosing function to a wait in a test callback', () => {
    const findings = analyze({ 'spec.ts': 'test(\'case\', async ({ page }) => { await page.waitForTimeout(1) })' })
    expect(findings).toEqual([{ path: '/e2e/spec.ts', line: 1, enclosingFunction: '' }])
  })

  it('ignores the name in a comment, in a plain string, and in a definition', () => {
    const findings = analyze({
      'spec.ts': `// page.waitForTimeout(1) sizes a window with a sleep.
const hint = 'Do not call page.waitForTimeout'
const fake = { waitForTimeout: async () => {} }
const [waitForTimeout] = waits`,
    })
    expect(findings).toEqual([])
  })

  it('ignores the other wait methods of a page', () => {
    const findings = analyze({
      'spec.ts': `await page.waitForFunction(() => true)
await page.waitForSelector('#id')
await page.waitForTimeoutLater?.(1)`,
    })
    expect(findings).toEqual([])
  })
})

describe('e2e fixed waits', () => {
  const files = collectE2EFiles().map(path => ({ path, source: readFileSync(path, 'utf-8') }))
  const findings = fixedWaits(files)
  const key = (finding: { path: string, enclosingFunction: string }) => `${posixRelative(e2eRoot, finding.path)} ${finding.enclosingFunction}`

  it('scans the e2e tree', () => {
    expect(files.length).toBeGreaterThan(100)
  })

  it('never sizes a window with waitForTimeout', () => {
    const offenders = findings
      .filter(finding => !ACCEPTED.has(key(finding)))
      .map(finding => `${posixRelative(e2eRoot, finding.path)}:${finding.line}  in ${finding.enclosingFunction || 'a callback'}`)
    const hint = [
      'A fixed wait ends on schedule however late the state change comes, so it fails under load.',
      'Wait for the injected end of the transient state, or for a proof that cannot pass early',
      '(helpers/frames.ts waits for a rendered frame or for the end of a CSS transition):',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })

  it('keeps no accepted wait that the e2e tree no longer holds', () => {
    const found = new Set(findings.map(key))
    expect([...ACCEPTED.keys()].filter(entry => !found.has(entry))).toEqual([])
  })
})
