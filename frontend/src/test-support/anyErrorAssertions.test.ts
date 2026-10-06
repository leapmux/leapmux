import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { anyErrorAssertions } from '~/test-support/anyErrorAssertions'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// Test guard: no test in the frontend package may assert an error that any error satisfies, such as a bare
// `toThrow()`. Such an assertion passes when the code under test fails for a different reason, so the test no longer
// tests the reason that its title states. Require the specific error: a message substring or a regular expression that
// states the reason, the error class, or the fields of the error. `anyErrorAssertions.ts` holds the analysis.
//
// The scan covers each script source of the package, so it reaches each test that vitest or Playwright runs: `src/`,
// `tests/e2e/`, `scripts/`, `eslint/`, and the configuration tests at the root.

/** The extensions of a script source: TypeScript and JavaScript, with JSX and the module variants. */
const SCRIPT_SOURCE = /\.[cm]?[jt]sx?$/

/** The key of an accepted site: the path below the frontend root and the site of the assertion. */
function siteKey(path: string, site: string): string {
  return `${path} ${site}`
}

/**
 * The assertions that the guard accepts, by the key of `siteKey`, each with its reason. An entry that matches no
 * assertion fails the guard, so a converted assertion takes its entry with it.
 */
const ACCEPTED: ReadonlyMap<string, string> = new Map()

/** Analyze sources keyed by a path below a fixed root. */
function analyze(files: Record<string, string>) {
  return anyErrorAssertions(Object.entries(files).map(([path, source]) => ({ path: `/frontend/${path}`, source })))
}

describe('anyErrorAssertions', () => {
  it('finds a bare toThrow, with its line, its matcher, and the titles of its suite and its test', () => {
    const findings = analyze({
      'parse.test.ts': `describe('parse', () => {
  it('rejects an empty input', () => {
    expect(() => parse('')).toThrow()
  })
})`,
    })
    expect(findings).toEqual([{ path: '/frontend/parse.test.ts', line: 3, matcher: 'toThrow()', site: 'parse > rejects an empty input' }])
  })

  it('finds each throw matcher whose argument any error matches, on a function and on a rejection', () => {
    const findings = analyze({
      'spec.test.ts': `expect(run).toThrowError()
await expect(promise).rejects.toThrow()
await expect(promise).rejects.toThrowError()
expect(run).toThrow(undefined)
expect(run).toThrow('')
expect(run).toThrow(\`\`)
expect(run).toThrow(Error)
expect(run).toThrow(expect.any(Error))
expect(run).toThrow(expect.anything())
expect.soft(run).toThrow()`,
    })
    expect(findings.map(finding => [finding.line, finding.matcher])).toEqual([
      [1, 'toThrowError()'],
      [2, 'rejects.toThrow()'],
      [3, 'rejects.toThrowError()'],
      [4, 'toThrow(undefined)'],
      [5, 'toThrow(\'\')'],
      [6, 'toThrow(``)'],
      [7, 'toThrow(Error)'],
      [8, 'toThrow(expect.any(Error))'],
      [9, 'toThrow(expect.anything())'],
      [10, 'toThrow()'],
    ])
  })

  it('finds each matcher on a rejection that any error satisfies', () => {
    const findings = analyze({
      'spec.test.ts': `await expect(promise).rejects.toBeDefined()
await expect(promise).rejects.toBeTruthy()
await expect(promise).rejects.toBeInstanceOf(Error)
await expect(promise).rejects.toEqual(expect.any(Error))
await expect(promise).rejects.toStrictEqual(expect.anything())`,
    })
    expect(findings.map(finding => finding.matcher)).toEqual([
      'rejects.toBeDefined()',
      'rejects.toBeTruthy()',
      'rejects.toBeInstanceOf(Error)',
      'rejects.toEqual(expect.any(Error))',
      'rejects.toStrictEqual(expect.anything())',
    ])
  })

  it('reports the line of the matcher name in an assertion that spans lines', () => {
    const findings = analyze({ 'spec.test.ts': 'await expect(promise)\n  .rejects\n  .toThrow()' })
    expect(findings.map(finding => finding.line)).toEqual([3])
  })

  it('accepts a specific error: a message, a pattern, a class, the fields of the error, and a value', () => {
    const findings = analyze({
      'spec.test.ts': `expect(run).toThrow('tokenizer exceeded limit')
expect(run).toThrow(/^Unknown key/)
expect(run).toThrow(TypeError)
expect(run).toThrowError(new Error('closed'))
expect(run).toThrow(expect.objectContaining({ code: 'ENOENT' }))
expect(run).toThrow(expect.any(WorkerRpcError))
expect(run).toThrowErrorMatchingInlineSnapshot('"closed"')
await expect(promise).rejects.toThrow(/timed out/)
await expect(promise).rejects.toBeInstanceOf(WorkerRpcError)
await expect(promise).rejects.toMatchObject({ code: 'ENOENT' })
await expect(promise).rejects.toEqual(new Error('closed'))
await expect(promise).rejects.toBe(reason)
await expect(promise).rejects.toEqual(undefined)`,
    })
    expect(findings).toEqual([])
  })

  it('accepts a negated matcher, because an assertion that nothing throws is specific', () => {
    const findings = analyze({
      'spec.test.ts': `expect(run).not.toThrow()
expect(run).not.toThrowError()
await expect(promise).rejects.not.toThrow()
await expect(promise).resolves.not.toThrow()
await expect(promise).resolves.toBeDefined()
expect(value).toBeDefined()
expect(value).toBeInstanceOf(Error)`,
    })
    expect(findings).toEqual([])
  })

  it('ignores the matcher name in a comment, in a string, and in a definition', () => {
    const findings = analyze({
      'spec.test.ts': `// expect(run).toThrow() passes for every error.
const hint = 'Do not call expect(run).toThrow()'
const matchers = { toThrow() {} }
const { rejects } = settled`,
    })
    expect(findings).toEqual([])
  })

  it('takes the site from each suite and test that holds the assertion, with each spelling of the test call', () => {
    const findings = analyze({
      'spec.test.ts': `describe.concurrent('outer', () => {
  describe(\`inner\`, () => {
    it.each([1, 2])('case %i', (n) => { expect(() => run(n)).toThrow() })
    test.skipIf(slow)(title, () => { expect(run).toThrow() })
    it.only('only', () => { expect(run).toThrow() })
  })
})`,
    })
    expect(findings.map(finding => finding.site)).toEqual([
      'outer > inner > case %i',
      'outer > inner > title',
      'outer > inner > only',
    ])
  })

  it('takes the site from the enclosing function outside every suite, and gives an empty site to a bare statement', () => {
    const findings = analyze({
      'helpers.ts': `export function expectClosed(promise) {
  return expect(promise).rejects.toThrow()
}
expect(run).toThrow()`,
    })
    expect(findings.map(finding => finding.site)).toEqual(['expectClosed', ''])
  })

  it('ignores a call that is not a suite or a test, such as a loop over cases', () => {
    const findings = analyze({
      'spec.test.ts': `it('each input', () => {
  inputs.forEach(input => expect(() => parse(input)).toThrow())
})`,
    })
    expect(findings.map(finding => finding.site)).toEqual(['each input'])
  })

  it('parses JavaScript and JSX sources', () => {
    const findings = analyze({
      'script.test.mjs': 'await expect(readLog(path)).rejects.toThrow()',
      'View.test.tsx': 'it(\'renders\', () => { expect(() => render(() => <View />)).toThrow() })',
    })
    expect(findings.map(finding => finding.path)).toEqual(['/frontend/script.test.mjs', '/frontend/View.test.tsx'])
  })
})

describe('frontend any-error assertions', () => {
  const files = collectFiles(frontendRoot, { matches: name => SCRIPT_SOURCE.test(name) })
    .map(path => ({ path, source: readFileSync(path, 'utf-8') }))
  const findings = anyErrorAssertions(files)
  const key = (finding: { path: string, site: string }) => siteKey(posixRelative(frontendRoot, finding.path), finding.site)

  it('scans each tree that holds a test', () => {
    const scanned = files.map(file => posixRelative(frontendRoot, file.path))
    for (const tree of ['src/', 'tests/e2e/', 'scripts/', 'eslint/'])
      expect(scanned.filter(path => path.startsWith(tree)).length, tree).toBeGreaterThan(0)
    expect(scanned).toContain('playwright.config.test.ts')
    expect(files.length).toBeGreaterThan(1000)
  })

  it('never asserts an error that any error satisfies', () => {
    const offenders = findings
      .filter(finding => !ACCEPTED.has(key(finding)))
      .map(finding => `${posixRelative(frontendRoot, finding.path)}:${finding.line}  ${finding.matcher}  in ${finding.site || 'a statement outside every test'}`)
    const hint = [
      'Each of these assertions passes for any error, so the test passes when the code fails for a different reason.',
      'Require the specific error: a message substring or a regular expression that states the reason',
      '(toThrow(/^Unknown key/)), the error class (toThrow(WorkerRpcError)), or the fields of the error',
      '(rejects.toMatchObject({ code: \'ENOENT\' })):',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })

  it('keeps no accepted assertion that the tree no longer holds', () => {
    const found = new Set(findings.map(key))
    expect([...ACCEPTED.keys()].filter(entry => !found.has(entry))).toEqual([])
  })
})
