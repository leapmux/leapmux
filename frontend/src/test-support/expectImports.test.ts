import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'
import { passedOnExpects } from '~/test-support/expectImports'
import { posixRelative } from '~/test-support/sourceTree'

// E2E guard: a spec, a fixture, and a helper import the plain `expect` from `@playwright/test`, and no module passes it
// on. A module that extends `expect` exports a value of its own, and an import of it stays correct.
// `expectImports.ts` holds the analysis.

/** Analyze sources keyed by a path below a fixed root. */
function analyze(files: Record<string, string>) {
  return passedOnExpects(Object.entries(files).map(([path, source]) => ({ path: `/e2e/${path}`, source })))
}

describe('passedOnExpects', () => {
  it('finds a fixture module that re-exports the expect of Playwright, and each file that imports it there', () => {
    const findings = analyze({
      'fixtures.ts': `import { test as base, expect } from '@playwright/test'
export const test = base.extend({})
export { expect }`,
      'a.spec.ts': `import { expect, test } from './fixtures'`,
      'helpers/b.spec.ts': `import type { Page } from '@playwright/test'
import { expect } from '../fixtures'`,
      'c.spec.ts': `import {
  expect,
  test,
} from './fixtures'`,
    })
    expect(findings).toEqual([
      { path: '/e2e/fixtures.ts', line: 3, kind: 'reexport' },
      { path: '/e2e/a.spec.ts', line: 1, kind: 'import', module: './fixtures' },
      { path: '/e2e/helpers/b.spec.ts', line: 2, kind: 'import', module: '../fixtures' },
      { path: '/e2e/c.spec.ts', line: 1, kind: 'import', module: './fixtures' },
    ])
  })

  it('finds a re-export from Playwright itself and an import of it under an alias', () => {
    const findings = analyze({
      'reexport.ts': `export { expect, test } from '@playwright/test'`,
      'spec.ts': `import { expect as fixtureExpect } from './reexport'`,
    })
    expect(findings.map(finding => [finding.path, finding.kind])).toEqual([
      ['/e2e/reexport.ts', 'reexport'],
      ['/e2e/spec.ts', 'import'],
    ])
  })

  it('accepts a direct import, and an import of an expect that a module extends', () => {
    const findings = analyze({
      'extended.ts': `import { expect as base } from '@playwright/test'
export const expect = base.extend({ toBeQuiet() { return { pass: true, message: () => '' } } })`,
      'a.spec.ts': `import { expect } from './extended'`,
      'b.spec.ts': `import { expect } from '@playwright/test'
import { test } from './fixtures'`,
      'c.test.ts': `import { expect } from 'vitest'`,
      'fixtures.ts': `import { test as base } from '@playwright/test'
export const test = base.extend({})`,
    })
    expect(findings).toEqual([])
  })

  it('accepts an export of a local expect, and an import from a module outside the inputs', () => {
    const findings = analyze({
      'local.ts': `function expect(value: unknown) { return value }
export { expect }`,
      'spec.ts': `import { expect } from './elsewhere'`,
    })
    expect(findings).toEqual([])
  })
})

describe('e2e expect imports', () => {
  const files = collectE2EFiles().map(path => ({ path, source: readFileSync(path, 'utf-8') }))

  it('scans the e2e tree', () => {
    expect(files.length).toBeGreaterThan(100)
  })

  it('imports the plain expect from Playwright, never through a module that passes it on', () => {
    const offenders = passedOnExpects(files)
      .map(finding => `${posixRelative(e2eRoot, finding.path)}:${finding.line}  ${finding.kind === 'reexport' ? 're-exports expect' : `imports expect from ${finding.module}`}`)
    const hint = [
      'Import expect from @playwright/test. A fixture module exports its test object only,',
      'unless it extends expect into a value of its own:',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
