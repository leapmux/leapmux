import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

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

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const srcRoot = join(frontendRoot, 'src')

/** This file, which quotes the banned calls to describe them. */
const selfPath = fileURLToPath(import.meta.url)

/**
 * A `vi.mock` or `vi.doMock` call whose first argument is a string literal.
 * A call with a computed path is out of reach of a text scan, and rare enough
 * to leave to review.
 */
const MOCK_CALL = /\bvi\.(?:do)?Mock\(\s*(['"`])([^'"`]+)\1/g

/**
 * A line whose first non-space characters open or continue a comment. The ban
 * has to be EXPLAINED somewhere, and the explanation quotes the call, so a
 * comment is not an offence. A trailing comment on a line of code still
 * counts -- move the note above the line.
 */
const COMMENT_LINE = /^\s*(?:\/\/|\/\*|\*)/

function collectUnitTestFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...collectUnitTestFiles(full))
    else if (/\.test\.tsx?$/.test(entry.name) && full !== selfPath)
      found.push(full)
  }
  return found
}

function duplicateMockPaths(file: string): string[] {
  const counts = new Map<string, number>()
  for (const line of readFileSync(file, 'utf-8').split('\n')) {
    if (COMMENT_LINE.test(line))
      continue
    for (const match of line.matchAll(MOCK_CALL))
      counts.set(match[2], (counts.get(match[2]) ?? 0) + 1)
  }
  return [...counts].filter(([, count]) => count > 1).map(([path]) => path)
}

describe('unit-test module mocks', () => {
  it('registers each mocked path one time for each file', () => {
    const offenders: string[] = []
    for (const file of collectUnitTestFiles(srcRoot)) {
      for (const path of duplicateMockPaths(file))
        offenders.push(`${relative(frontendRoot, file)}  registers "${path}" more than once`)
    }
    const hint = [
      'Two queued registrations of one path have no defined winner: vitest resolves the queued',
      'mocks of a group in parallel and registers each one as its resolve call returns, so the',
      'last resolve wins rather than the last line. Register the path once and vary the behavior',
      'through the spy (one vi.fn at describe scope, mockImplementation for each test):',
    ].join(' ')
    expect(offenders, `${hint}\n  ${offenders.join('\n  ')}`).toEqual([])
  })
})
