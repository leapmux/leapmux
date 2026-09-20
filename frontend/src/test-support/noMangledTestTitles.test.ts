import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// Test-name guard: `test/prefer-lowercase-title` (from the antfu ESLint config)
// rejects a CASE title that starts with a capital, and its `--fix` lowercases
// character 0 and nothing else. A title that opens with a name which must keep
// its capital therefore comes back misspelled -- `DEFAULT_MONO_FONT_FAMILY`
// became `dEFAULT_MONO_FONT_FAMILY`, `OAuthCompleteSignupPage` became
// `oAuthCompleteSignupPage`, `IPv4` became `iPv4`. Eleven such names accumulated
// across the suite before anyone noticed, because each one reads as a plausible
// identifier at a glance and the fix leaves the file lint-clean.
//
// A `describe` is exempt from that rule now (see `eslint.config.ts`), because a
// suite identifies the SYMBOL under test and must be able to spell it. Both signatures
// below still cover describe titles: the rule is off, and the damage it already
// did is not.
//
// This guard fails the suite on the mangle signature itself: a title whose
// first character is lowercase and whose second is uppercase. That is what the
// autofix produces and what no deliberate title looks like, apart from a real
// camelCase identifier -- see CAMEL_CASE_IDENTIFIERS below.
//
// It then checks a SECOND signature that the autofix cannot make, and that this
// one is blind to: a name whose capitals were ALL removed by hand, which leaves
// a lowercase run with no uppercase second letter to catch. See the block above
// `findFlattenedTitles`.
//
// It covers `tests/e2e/` as well as `src/`. The ESLint rule does not run on
// Playwright specs at all, so a mangled title copied into one would otherwise
// have nothing checking it.
//
// Written as a test rather than a custom ESLint rule for the same reason
// `noMirroredUnitTests.test.ts` and `noControlBytesInSource.test.ts` are: it
// runs in the `bun run test` everyone already runs, it needs no plugin
// scaffolding, and it reports every offender in one message.

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
    if (ch !== undefined && QUOTES.has(ch)) {
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
  while (i < source.length && /\s/.test(source[i] ?? ''))
    i++
  if (!QUOTES.has(source[i] ?? ''))
    return null
  const end = skipString(source, i)
  return { title: source.slice(i + 1, end - 1), start: i + 1 }
}

/** Every suite and case title in one file, with the line each one sits on. */
function findTitles(source: string): Array<{ title: string, line: number }> {
  const found: Array<{ title: string, line: number }> = []
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
    if (read !== null)
      found.push({ title: read.title, line: source.slice(0, read.start).split('\n').length })
    match = scanner.exec(source)
  }
  return found
}

/** Extracts every suite and case title from one file and keeps the mangled ones. */
export function findMangledTitles(source: string, path: string): MangledTitle[] {
  return findTitles(source)
    .filter(found => isMangledTitle(found.title))
    .map(found => ({ path, ...found }))
}

// The SECOND signature, and nothing about the lint autofix produces it. A title
// whose first word is a known identifier with every capital removed --
// `mcpToolCallDisplayName` written `mcptoolcalldisplayname` -- reads as a word
// nobody can search for, and 162 of them accumulated. The autofix cannot make
// one, because it only ever lowercases character 0.
//
// "Known" is what keeps this precise. A long lowercase first word is no evidence
// on its own: `describe('classifies an empty payload')` opens with ten lowercase
// letters and is exactly right, and `describe('lostpointercapture')` states a DOM
// event whose real spelling has no capitals at all. So the rule asks a different
// question -- does this word match an identifier THIS FILE already knows? -- and
// the answer comes from the file's own imports plus the module it sits beside.

/** An identifier built from more than one word, which is the only kind a title can flatten. */
const MULTI_WORD = /[a-z][A-Z]|_/

/**
 * Whether a name carries more than one word, so a title could have flattened it.
 *
 * A lowercase-to-uppercase step or an underscore, over the WHOLE name. NOT "holds a
 * capital after the first character": that reads `PI`, `CODEX` and `ZCODE` as
 * multi-word, and a suite legitimately called `describe('pi tool presentation')` would
 * then be rejected for flattening a constant it never mentions. It also keeps a
 * one-word component out: `describe('tooltip')` beside `Tooltip` is an ordinary title.
 */
function isMultiWord(name: string): boolean {
  return MULTI_WORD.test(name)
}

/**
 * The local binding of every import in one file: named, aliased, default and namespace.
 *
 * The LOCAL name, because that is the one a title would refer to. An import written
 * `{ parseMcpToolName as parseName }` is known here as `parseName`.
 */
function importedNames(source: string): string[] {
  const names: string[] = []
  // No `\s+` on either side of the capture: whitespace is inside `[^'"]`, so the two
  // could exchange characters and `regexp/no-super-linear-backtracking` rejects that.
  // The word boundaries do the same job and cannot backtrack. `\bfrom\b` is lazy-bounded
  // and anchored on the quote that follows it, so `{ fromEntries }` and even
  // `{ from }` reach the real specifier -- that quote is the whole test, which is why
  // `from` needs no closing `\b` of its own.
  for (const clause of source.matchAll(/\bimport\b([^'"]+?)\bfrom\s*['"]/g)) {
    const text = clause[1]
    if (text === undefined)
      continue
    for (const namespace of text.matchAll(/\*\s+as\s+([\w$]+)/g)) {
      const name = namespace[1]
      if (name !== undefined)
        names.push(name)
    }
    const braces = /\{([^}]*)\}/.exec(text)
    for (const entry of (braces?.[1] ?? '').split(',')) {
      const binding = entry.trim().replace(/^type\s+/, '')
      if (binding)
        names.push(/\bas\s+([\w$]+)$/.exec(binding)?.[1] ?? binding)
    }
    const head = text.split(/[{,]/)[0]?.trim() ?? ''
    if (/^[\w$]+$/.test(head))
      names.push(head)
  }
  return names
}

/** Every title whose first word is a known multi-word identifier with its capitals dropped. */
export function findFlattenedTitles(source: string, path: string): MangledTitle[] {
  const sibling = path.split('/').at(-1)?.replace(TEST_FILE, '') ?? ''
  const known = new Map<string, string>()
  for (const name of [...importedNames(source), sibling]) {
    if (isMultiWord(name))
      known.set(name.toLowerCase(), name)
  }
  return findTitles(source).flatMap((found) => {
    const firstWord = FIRST_WORD.exec(found.title)?.[0] ?? ''
    // Entirely lowercase, so a title that merely opens with a lowercase letter --
    // every legal one does -- is not evidence. Only a word that dropped the
    // capitals it should carry reaches the lookup.
    if (firstWord === '' || firstWord !== firstWord.toLowerCase())
      return []
    const correct = known.get(firstWord)
    return correct === undefined || correct === firstWord ? [] : [{ path, ...found }]
  })
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
      offenders.push(...findMangledTitles(readFileSync(file, 'utf8'), posixRelative(frontendRoot, file)))

    const detail = offenders.map(o => `${o.path}:${o.line}  ${JSON.stringify(o.title)}`).join('\n  ')
    expect(
      offenders,
      'A title must never start with a name that keeps its capital: `test/prefer-lowercase-title` '
      + 'lowercases the first letter alone, so `--fix` returns the name misspelled '
      + '(DEFAULT_MONO_FONT_FAMILY -> dEFAULT_MONO_FONT_FAMILY). Do not keep the mangled spelling and '
      + 'do not suppress the lint rule. Lead with a lowercase phrase and write the name in full after '
      + 'it, per the vitest rule in CLAUDE.md: '
      + '`describe(\'default mono font stack (DEFAULT_MONO_FONT_FAMILY)\')`. If the first word is a '
      + `genuinely camelCase identifier, add it to CAMEL_CASE_IDENTIFIERS in ${posixRelative(frontendRoot, fileURLToPath(import.meta.url))} `
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

// Builds an import line without writing one into this file, for the same reason
// `callSite` exists: the tree scan reads this module too.
function importLine(names: string, from = './module'): string {
  return ['import', `{ ${names} }`, 'from', `'${from}'`].join(' ')
}

describe('test-title flattening', () => {
  it('has no title that dropped an identifier\'s capitals, under src/ or tests/', () => {
    const offenders: MangledTitle[] = []
    for (const file of collectTestFiles())
      offenders.push(...findFlattenedTitles(readFileSync(file, 'utf8'), posixRelative(frontendRoot, file)))

    const detail = offenders.map(o => `${o.path}:${o.line}  ${JSON.stringify(o.title)}`).join('\n  ')
    expect(
      offenders,
      'A title opened with a name this file knows, spelled with every capital removed '
      + '(mcpToolCallDisplayName -> mcptoolcalldisplayname). Nobody can search for that word, and '
      + 'no lint rule produces it. Write the identifier\'s own casing when it starts with a '
      + 'lowercase letter (`describe(\'mcpToolCallDisplayName\')`); when it starts with a capital, '
      + 'lead with a lowercase phrase and put the name after it '
      + `(\`describe('the chat row skeleton (ChatRowSkeleton)')\`):\n  ${detail}`,
    ).toEqual([])
  })

  it('reports a flattened import, with the line it sits on', () => {
    const source = [importLine('mcpToolCallDisplayName'), '', callSite('describe', 'mcptoolcalldisplayname')].join('\n')
    expect(findFlattenedTitles(source, 'x.test.ts')).toEqual([
      { path: 'x.test.ts', line: 3, title: 'mcptoolcalldisplayname' },
    ])
  })

  it('reports a flattened module name, which no import states', () => {
    const source = callSite('describe', 'chatpremeasurebands')
    expect(findFlattenedTitles(source, 'chatPremeasureBands.test.ts')).toHaveLength(1)
    // The same title beside a module whose name it does not flatten is nobody's business.
    expect(findFlattenedTitles(source, 'unrelated.test.ts')).toEqual([])
  })

  it('reads the LOCAL name of an aliased import', () => {
    const source = [importLine('parseMcpToolName as parseToolName'), callSite('it', 'parsetoolname splits the pair')].join('\n')
    expect(findFlattenedTitles(source, 'x.test.ts')).toHaveLength(1)
    // `parsemcptoolname` is the name at the OTHER end of the alias, which this file
    // never binds, so a title that used it would describe something else.
    expect(findFlattenedTitles(callSite('it', 'parsemcptoolname splits the pair'), 'x.test.ts')).toEqual([])
  })

  /**
   * The three shapes a long lowercase first word takes that are NOT a defect.
   *
   * Each one is why this rule asks about the file's own identifiers rather than about
   * the word's length: an ordinary sentence, a real name that carries no capital, and a
   * one-word identifier whose lowercase form is the correct title.
   */
  it('accepts an ordinary phrase, a genuinely lowercase name, and a one-word identifier', () => {
    expect(findFlattenedTitles(callSite('it', 'classifies an empty payload'), 'x.test.ts')).toEqual([])
    const domEvent = [importLine('releasePointer'), callSite('describe', 'lostpointercapture')].join('\n')
    expect(findFlattenedTitles(domEvent, 'x.test.ts')).toEqual([])
    const oneWord = [importLine('Tooltip'), callSite('describe', 'tooltip')].join('\n')
    expect(findFlattenedTitles(oneWord, 'Tooltip.test.tsx')).toEqual([])
  })

  /**
   * A name in one case throughout is one word, whatever its case is.
   *
   * `PI` and `CODEX` hold a capital after character 0, so a multi-word test that asked
   * only that would read them as two words and reject the 28 suites called
   * `describe('pi ...')` for flattening a constant none of them mentions.
   */
  it('accepts a lowercase word that folds onto an all-caps constant', () => {
    const source = [importLine('PI_EVENT, PI'), callSite('describe', 'pi tool presentation')].join('\n')
    expect(findFlattenedTitles(source, 'createToolCall.test.ts')).toEqual([])
  })

  it('accepts the identifier written in its own casing', () => {
    const camel = [importLine('mcpToolCallDisplayName'), callSite('describe', 'mcpToolCallDisplayName')].join('\n')
    expect(findFlattenedTitles(camel, 'x.test.ts')).toEqual([])
    // A `describe` leads with a PascalCase name now that the lint rule ignores describe
    // titles, and this rule asks for exactly the spelling the file binds.
    const pascal = [importLine('ChatRowSkeleton'), callSite('describe', 'ChatRowSkeleton')].join('\n')
    expect(findFlattenedTitles(pascal, 'ChatRowSkeleton.test.tsx')).toEqual([])
  })

  it('ignores a name that no import and no sibling module states', () => {
    expect(findFlattenedTitles(callSite('describe', 'sometotallyunknownthing'), 'x.test.ts')).toEqual([])
  })
})
