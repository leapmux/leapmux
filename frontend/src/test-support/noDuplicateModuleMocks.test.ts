import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { lineNumberAt, stripCommentLines } from '~/test-support/sourceScan'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// Unit-test guard: each test file registers a mocked path one time only.
//
// A second registration of one path does NOT reliably replace the first one.
// `vi.mock` and `vi.doMock` register nothing by themselves -- they push the
// path onto a queue. Before the next import, vitest groups the queue by
// action, resolves every mock of a group in parallel, and registers each
// result when its own resolve call returns. The registration that wins is the
// resolve call that finishes last, not the call that the file wrote last.
//
// The failure is a heisenbug. Each factory is correct on its own, the wrong
// one runs, and the assertion fails somewhere unrelated. The boot-handoff test
// for `entry-client` paired a `beforeEach` mock of `@solidjs/start/client`
// with a per-test mock of the same path. It passed on macOS, Windows and linux
// amd64, and failed on the linux arm64 runner, where the full suite starves
// the two resolve calls unevenly.
//
// Register the path one time, and vary the behavior through the spy: keep one
// `vi.fn` at describe scope, give it to the single factory, and let each test
// call `mockImplementation` on it. `src/entry-client.test.ts` shows the shape.

const srcRoot = join(frontendRoot, 'src')

/**
 * A `vi.mock` or `vi.doMock` call whose first argument is a string literal.
 *
 * Spell both names out. `vi\.(?:do)?Mock\(` reads as if it covered the pair,
 * but it matches `vi.doMock` and a `vi.Mock` that no API supplies. The name
 * that nearly every file uses is `vi.mock`, with a lower-case m, so the
 * collapsed form walks the whole tree and reports almost nothing.
 *
 * The scan runs over the whole file rather than line by line, so the `\s*`
 * spans the break a formatter puts after the paren. A call with a computed
 * path is out of reach of a text scan, and rare enough to leave to review.
 */
const MOCK_CALL = /\bvi\.(?:mock|doMock)\(\s*(['"`])([^'"`]+)\1/g

export interface DuplicateMock {
  /** The mocked path, exactly as the calls spell it. */
  path: string
  /** The 1-based line of each call that registers the path, in file order. */
  lines: number[]
}

/**
 * Every path that `source` registers more than one time, with the offending
 * lines.
 *
 * Takes the SOURCE rather than a path, so the cases below can drive it with a
 * fixture instead of a file on disk.
 */
export function findDuplicateMocks(source: string): DuplicateMock[] {
  const code = stripCommentLines(source)
  const linesByPath = new Map<string, number[]>()
  for (const match of code.matchAll(MOCK_CALL)) {
    const path = match[2]
    const lines = linesByPath.get(path) ?? []
    lines.push(lineNumberAt(code, match.index))
    linesByPath.set(path, lines)
  }
  return [...linesByPath]
    .filter(([, lines]) => lines.length > 1)
    .map(([path, lines]) => ({ path, lines }))
}

const UNIT_TEST_FILE = /\.test\.tsx?$/

/** Every co-located unit test under `src/`, as an absolute path. */
function collectSrcTestFiles(): string[] {
  return collectFiles(srcRoot, { matches: name => UNIT_TEST_FILE.test(name) })
}

/**
 * A `vi.mock` or `vi.doMock` call, BUILT rather than written, so the tree scan
 * does not read this file's own fixtures as real offences. `wrapped` puts the
 * path on its own line, the way a formatter breaks a long call.
 */
function mockCall(fn: 'mock' | 'doMock', path: string, wrapped = false): string {
  const argument = JSON.stringify(path)
  return wrapped
    ? `vi.${fn}(\n  ${argument},\n  () => ({}),\n)`
    : `vi.${fn}(${argument}, () => ({}))`
}

/** A call form that must never count: an unmock, or the `vi.mocked` helper. */
function nonMockCall(fn: 'unmock' | 'doUnmock' | 'mocked', path: string): string {
  return `vi.${fn}(${JSON.stringify(path)})`
}

describe('unit-test module mocks', () => {
  it('registers each mocked path one time for each file', () => {
    const offenders: string[] = []
    for (const file of collectSrcTestFiles()) {
      for (const { path, lines } of findDuplicateMocks(readFileSync(file, 'utf-8')))
        offenders.push(`${posixRelative(frontendRoot, file)}:${lines.join(',')}  registers "${path}" ${lines.length} times`)
    }
    const hint = [
      'Two queued registrations of one path have no defined winner: vitest resolves the queued',
      'mocks of a group in parallel and registers each one as its resolve call returns, so the',
      'last resolve wins rather than the last line. Register the path once and vary the behavior',
      'through the spy (one vi.fn at describe scope, mockImplementation for each test):',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })

  it('finds the files it is meant to be guarding', () => {
    // Without this the case above passes vacuously the day the layout moves or
    // the walk stops matching, which is exactly when it needs to fail.
    expect(collectSrcTestFiles().length, 'no unit test found -- has the layout moved?')
      .toBeGreaterThanOrEqual(100)
  })

  it('reports a path that two vi.mock calls register, with both lines', () => {
    const source = [
      mockCall('mock', '~/lib/a'),
      mockCall('mock', '~/lib/b'),
      mockCall('mock', '~/lib/a'),
    ].join('\n')

    expect(findDuplicateMocks(source)).toEqual([{ path: '~/lib/a', lines: [1, 3] }])
  })

  it('reports a path that vi.mock and vi.doMock register once each', () => {
    // The shape that started this: a hoisted mock of the path, plus a second
    // one installed for a single case.
    const source = [mockCall('mock', '~/lib/a'), mockCall('doMock', '~/lib/a')].join('\n')
    expect(findDuplicateMocks(source)).toEqual([{ path: '~/lib/a', lines: [1, 2] }])
  })

  it('accepts one registration for each path', () => {
    const source = [mockCall('doMock', '~/lib/a'), mockCall('mock', '~/lib/b')].join('\n')
    expect(findDuplicateMocks(source)).toEqual([])
  })

  it('counts a call that the formatter wrapped across lines', () => {
    // The scan reads the whole file, so the break after the paren does not
    // hide the second registration.
    const source = [mockCall('doMock', '~/lib/a'), mockCall('doMock', '~/lib/a', true)].join('\n')
    expect(findDuplicateMocks(source)).toEqual([{ path: '~/lib/a', lines: [1, 2] }])
  })

  it('ignores a call that a comment quotes', () => {
    const source = [
      mockCall('doMock', '~/lib/a'),
      `// ${mockCall('doMock', '~/lib/a')}`,
      ` * ${mockCall('doMock', '~/lib/a')}`,
    ].join('\n')

    expect(findDuplicateMocks(source)).toEqual([])
  })

  it('ignores an unmock and the vi.mocked helper', () => {
    const source = [
      mockCall('doMock', '~/lib/a'),
      nonMockCall('unmock', '~/lib/a'),
      nonMockCall('doUnmock', '~/lib/a'),
      nonMockCall('mocked', '~/lib/a'),
    ].join('\n')

    expect(findDuplicateMocks(source)).toEqual([])
  })

  it('keeps no regex state between calls', () => {
    // MOCK_CALL is a /g/ literal shared by every call. `matchAll` clones it
    // rather than advancing its `lastIndex`, and this pins that.
    const source = [mockCall('doMock', '~/lib/a'), mockCall('mock', '~/lib/a')].join('\n')
    expect(findDuplicateMocks(source)).toHaveLength(1)
    expect(findDuplicateMocks(source)).toHaveLength(1)
  })
})
