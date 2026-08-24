import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectFiles } from '~/test-support/sourceTree'

// Test-name guard: `test/prefer-lowercase-title` (from the antfu ESLint config)
// rejects a suite or case title that starts with a capital, and its `--fix`
// lowercases character 0 and nothing else. A title that opens with a name which
// must keep its capital therefore comes back misspelled --
// `DEFAULT_MONO_FONT_FAMILY` became `dEFAULT_MONO_FONT_FAMILY`,
// `OAuthCompleteSignupPage` became `oAuthCompleteSignupPage`, `IPv4` became
// `iPv4`. Eleven such names accumulated across the suite before anyone noticed,
// because each one reads as a plausible identifier at a glance and the fix
// leaves the file lint-clean.
//
// This guard fails the suite on the mangle signature itself: a title whose
// first character is lowercase and whose second is uppercase. That is what the
// autofix produces and what no deliberate title looks like, apart from a real
// camelCase identifier -- see CAMEL_CASE_IDENTIFIERS below.
//
// It covers `tests/e2e/` as well as `src/`. The ESLint rule does not run on
// Playwright specs at all, so a mangled title copied into one would otherwise
// have nothing checking it.
//
// Written as a test rather than a custom ESLint rule for the same reason
// `noMirroredUnitTests.test.ts` and `noControlBytesInSource.test.ts` are: it
// runs in the `bun run test` everyone already runs, it needs no plugin
// scaffolding, and it reports every offender in one message.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const SOURCE_ROOTS = ['src', 'tests']
const TEST_FILE = /\.(?:test|spec)\.(?:ts|tsx)$/

// A title whose first word is a genuinely camelCase identifier trips the same
// signature, so each one is exempt by name and by evidence. Confirm the
// identifier's own casing in the module under test before you add an entry: a
// future `xAxis` belongs here, `iPv4` does not.
const CAMEL_CASE_IDENTIFIERS: Record<string, string> = {
  zOrder: 'src/stores/floatingWindow.store.ts declares `const [zOrder, setZOrder]`',
}

// The modifiers vitest and Playwright chain onto a suite or a case. Listing
// them keeps the scanner off unrelated members, such as `it.next(...)` on an
// iterator or `it.getAsFile(...)` on a DataTransferItem.
const MODIFIERS = 'describe|each|only|skip|todo|concurrent|sequential|fails|runIf|skipIf|for|extend'

// `describe(`, `it(`, `test(` and any chained form, up to the opening paren.
// The leading class rejects a member access on some other object
// (`suite.describe(`) while still matching at the start of a line.
const TITLE_CALL = new RegExp(
  String.raw`(?:^|[^.\w$])(?:describe|it|test)(?:\.(?:${MODIFIERS}))*\s*\(`,
  'g',
)

// The autofix lowercases the first letter alone, so an uppercase second letter
// is the fingerprint of a name that lost its capital.
const MANGLE_SIGNATURE = /^[a-z][A-Z]/
const FIRST_WORD = /^[a-z_$][\w$]*/i
const QUOTES = new Set(['\'', '"', '`'])

export interface MangledTitle {
  path: string
  line: number
  title: string
}

/** Reports whether a title carries the autofix mangle and is not an allowlisted identifier. */
export function isMangledTitle(title: string): boolean {
  if (!MANGLE_SIGNATURE.test(title))
    return false
  const firstWord = FIRST_WORD.exec(title)?.[0] ?? ''
  return !Object.hasOwn(CAMEL_CASE_IDENTIFIERS, firstWord)
}

/** Returns the index after the string literal that opens at `open`, escapes included. */
function skipString(source: string, open: number): number {
  const quote = source[open]
  for (let i = open + 1; i < source.length; i++) {
    if (source[i] === '\\') {
      i++
      continue
    }
    if (source[i] === quote)
      return i + 1
  }
  return source.length
}

/**
 * Returns the index after the `)` that closes the group opening at `open`.
 *
 * Steps over a nested string literal, so a paren inside an `it.each` fixture
 * row does not unbalance the count.
 */
function skipParens(source: string, open: number): number {
  let depth = 0
  for (let i = open; i < source.length; i++) {
    const ch = source[i]
    if (QUOTES.has(ch)) {
      i = skipString(source, i) - 1
      continue
    }
    if (ch === '(') {
      depth++
    }
    else if (ch === ')') {
      depth--
      if (depth === 0)
        return i + 1
    }
  }
  return source.length
}

/** Reads the quoted title at or after `from`, or null when that argument is not a string literal. */
function readTitle(source: string, from: number): { title: string, start: number } | null {
  let i = from
  while (i < source.length && /\s/.test(source[i]))
    i++
  if (!QUOTES.has(source[i]))
    return null
  const end = skipString(source, i)
  return { title: source.slice(i + 1, end - 1), start: i + 1 }
}

/** Extracts every suite and case title from one file and keeps the mangled ones. */
export function findMangledTitles(source: string, path: string): MangledTitle[] {
  const found: MangledTitle[] = []
  // A fresh RegExp per call: a shared /g/ literal carries `lastIndex` between
  // calls and would skip matches in the next file.
  const scanner = new RegExp(TITLE_CALL.source, 'g')
  let match = scanner.exec(source)
  while (match !== null) {
    const openParen = match.index + match[0].length - 1
    // `it.each(rows)('title', fn)` carries the title in a second call, so when
    // the first argument is not a string literal, look in the group that
    // follows the first one.
    let read = readTitle(source, openParen + 1)
    if (read === null) {
      const afterFirstCall = skipParens(source, openParen)
      if (source[afterFirstCall] === '(')
        read = readTitle(source, afterFirstCall + 1)
    }
    if (read !== null && isMangledTitle(read.title))
      found.push({ path, line: source.slice(0, read.start).split('\n').length, title: read.title })
    match = scanner.exec(source)
  }
  return found
}

/** Every unit test and e2e spec under the guarded roots, as an absolute path. */
function collectTestFiles(): string[] {
  return SOURCE_ROOTS.flatMap(root =>
    collectFiles(join(frontendRoot, root), { matches: name => TEST_FILE.test(name) }),
  )
}

// The historical offenders, exactly as they stood before the repair. They are
// the guard's regression corpus: each one must stay detected, because each one
// survived review once already.
const MANGLED_BY_THE_AUTOFIX = [
  'dEFAULT_MONO_FONT_FAMILY',
  'lANGUAGES',
  'oAuthCompleteSignupPage',
  'iPv4',
  'iPv6',
  'aND (&&)',
  'oR (||)',
  'sPLIT with one live child renders as that child, keeping the child own id',
  'jSX element trigger is wrapped in a div with display:contents',
  'oRs across a mixed group rather than picking one verdict',
  'dOES seed git info when the new terminal shares the active tab directory',
]

// The repaired names, plus the camelCase title that must stay legal.
const ACCEPTED = [
  'default mono font stack (DEFAULT_MONO_FONT_FAMILY)',
  'language option list (LANGUAGES)',
  'signup completion page (OAuthCompleteSignupPage)',
  'in IPv4',
  'in IPv6',
  'logical and (&&)',
  'logical or (||)',
  'a SPLIT with one live child renders as that child, keeping the child\'s own id',
  'a JSX element trigger is wrapped in a div with display:contents',
  'combines a mixed group with OR rather than picking one verdict',
  'does seed git info when the new terminal shares the active tab directory',
  'zOrder stale-id sweep',
  'editable-host selector (CONTENT_EDITABLE_SELECTOR)',
  'pi event constants (PI_EVENT)',
  'createStableContext',
  'channelManager openChannel',
  '',
]

// Builds a call site without writing `describe('` into this file, which the
// tree scan above would otherwise read as a real offender.
function callSite(fn: string, title: string): string {
  return `${fn}(${JSON.stringify(title)}, () => {})`
}

describe('test-title casing', () => {
  it('has no title that the lint autofix mangled, under src/ or tests/', () => {
    const offenders: MangledTitle[] = []
    for (const file of collectTestFiles())
      offenders.push(...findMangledTitles(readFileSync(file, 'utf8'), relative(frontendRoot, file)))

    const detail = offenders.map(o => `${o.path}:${o.line}  ${JSON.stringify(o.title)}`).join('\n  ')
    expect(
      offenders,
      'A title must never start with a name that keeps its capital: `test/prefer-lowercase-title` '
      + 'lowercases the first letter alone, so `--fix` returns the name misspelled '
      + '(DEFAULT_MONO_FONT_FAMILY -> dEFAULT_MONO_FONT_FAMILY). Do not keep the mangled spelling and '
      + 'do not suppress the lint rule. Lead with a lowercase phrase and write the name in full after '
      + 'it, per the vitest rule in CLAUDE.md: '
      + '`describe(\'default mono font stack (DEFAULT_MONO_FONT_FAMILY)\')`. If the first word is a '
      + `genuinely camelCase identifier, add it to CAMEL_CASE_IDENTIFIERS in ${relative(frontendRoot, fileURLToPath(import.meta.url))} `
      + `with the evidence:\n  ${detail}`,
    ).toEqual([])
  })

  it('finds the files it is meant to be guarding', () => {
    // Without this the case above passes vacuously the day a root moves or the
    // extension list stops matching, which is exactly when it needs to fail.
    expect(collectTestFiles().length, 'no test file found -- has the layout moved?')
      .toBeGreaterThanOrEqual(100)
  })

  it('detects every title the autofix mangled before the repair', () => {
    const mangled = MANGLED_BY_THE_AUTOFIX.filter(isMangledTitle)
    expect(mangled).toEqual(MANGLED_BY_THE_AUTOFIX)
  })

  it('accepts the repaired titles and a genuinely camelCase one', () => {
    expect(ACCEPTED.filter(isMangledTitle)).toEqual([])
  })

  it('exempts an allowlisted camelCase identifier by its whole first word', () => {
    // `zOrder` is allowlisted; `xAxis` has the same shape and is not, so a new
    // one must be added deliberately rather than matched by a wider pattern.
    expect(isMangledTitle('zOrder stale-id sweep')).toBe(false)
    expect(isMangledTitle('xAxis stale-id sweep')).toBe(true)
    // The allowlist matches the whole first word, never a prefix of it.
    expect(isMangledTitle('zOrderish thing')).toBe(true)
  })

  it('finds a mangled title in every call form, with its line number', () => {
    const source = [
      'import { describe, it } from \'vitest\'',
      callSite('describe', 'dEFAULT_MONO_FONT_FAMILY'),
      callSite('it', 'jSX element trigger is wrapped'),
      callSite('test', 'iPv4'),
      callSite('test.describe', 'oAuthCompleteSignupPage'),
      callSite('it.each([1])', 'sPLIT renders'),
      callSite('describe.skip', 'lANGUAGES'),
    ].join('\n')

    expect(findMangledTitles(source, 'fixture.test.ts')).toEqual([
      { path: 'fixture.test.ts', line: 2, title: 'dEFAULT_MONO_FONT_FAMILY' },
      { path: 'fixture.test.ts', line: 3, title: 'jSX element trigger is wrapped' },
      { path: 'fixture.test.ts', line: 4, title: 'iPv4' },
      { path: 'fixture.test.ts', line: 5, title: 'oAuthCompleteSignupPage' },
      { path: 'fixture.test.ts', line: 6, title: 'sPLIT renders' },
      { path: 'fixture.test.ts', line: 7, title: 'lANGUAGES' },
    ])
  })

  it('ignores a member call that only looks like a case', () => {
    // `it` and `test` are ordinary variable names elsewhere; a method on one of
    // them is not a test case, and its string argument is not a title.
    const source = [
      callSite('iterator.next', 'sOMETHING odd'),
      callSite('item.getAsFile', 'dOES not matter'),
      callSite('suite.describe', 'nOT a suite'),
    ].join('\n')

    expect(findMangledTitles(source, 'fixture.test.ts')).toEqual([])
  })

  it('keeps no scan state between files', () => {
    const source = callSite('describe', 'lANGUAGES')
    expect(findMangledTitles(source, 'a.test.ts')).toHaveLength(1)
    expect(findMangledTitles(source, 'b.test.ts')).toHaveLength(1)
  })
})
