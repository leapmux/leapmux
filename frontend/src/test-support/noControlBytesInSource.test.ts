import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Repo hygiene guard: a NUL byte (0x00) anywhere in a tracked source file makes
// Git classify that file as BINARY. Once it does, `git diff` and `git show`
// print only "Bin 3046 -> 3083 bytes" instead of a hunk, `git blame` stops
// working, and line-level merges degrade to whole-file conflicts. The practical
// consequence is that every change to the file ships unreviewable -- which is
// exactly what happened to `useWorkspaceLoader.ts`, whose loader was rewritten
// (a reactive `createEffect` became a one-shot `onMount`) with no visible diff.
//
// The trigger is easy to reintroduce by accident: a composite-key separator
// written as a literal NUL inside a template literal rather than as the `\u0000`
// escape. The two are byte-identical to the JS engine and visually identical in
// most editors, so nothing but a check like this one distinguishes them.
//
// Written as a test rather than a lint rule for the same reason
// `noMirroredUnitTests.test.ts` is: it runs in CI on every change with no extra
// tooling, and it fails loudly with the offending path and byte offset.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const SOURCE_ROOTS = ['src', 'tests']
const SKIP_DIRS = new Set(['node_modules', '.output', '.vinxi', 'dist', 'test-results'])
const SOURCE_FILE = /\.(?:ts|tsx|js|jsx|css|json|md)$/

interface Offender {
  path: string
  offset: number
}

function collectOffenders(dir: string, found: Offender[]): void {
  if (!existsSync(dir))
    return
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (SKIP_DIRS.has(entry.name))
      continue
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      collectOffenders(full, found)
      continue
    }
    if (!SOURCE_FILE.test(entry.name))
      continue
    const offset = readFileSync(full).indexOf(0x00)
    if (offset !== -1)
      found.push({ path: relative(frontendRoot, full), offset })
  }
}

describe('source hygiene', () => {
  it('has no NUL bytes in tracked source (git treats such files as binary and hides their diffs)', () => {
    const offenders: Offender[] = []
    for (const root of SOURCE_ROOTS)
      collectOffenders(join(frontendRoot, root), offenders)

    const detail = offenders.map(o => `${o.path} (byte ${o.offset})`).join('\n  ')
    expect(
      offenders,
      `A NUL byte makes Git treat the file as binary, so its diffs, blame and line-level merges stop working and every future change ships unreviewable. Write the escape \`\\u0000\` instead of a literal NUL -- the two are identical at runtime:\n  ${detail}`,
    ).toEqual([])
  })
})
