import { existsSync, lstatSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'bun:test'

/**
 * The project rules live in `AGENTS.md` alone. `CLAUDE.md` exists only to import it.
 *
 * Claude Code loads `CLAUDE.md` by itself. It loads `AGENTS.md` only through its built-in
 * `agents-md` plugin, and in its default mode that plugin loads nothing for a project that
 * has a `CLAUDE.md` of its own. The one-line import makes the engine load `AGENTS.md` with no
 * plugin, so a Claude Code version without the plugin loads it also. Other agent harnesses
 * read `AGENTS.md` directly.
 *
 * A rule written into `CLAUDE.md` is a second copy that the other harnesses never read, so
 * this test rejects any other content. A root `.claude/CLAUDE.md` is a second copy also,
 * because the engine loads it as project instructions.
 */

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')

describe('CLAUDE.md', () => {
  it('holds only the import of AGENTS.md', () => {
    expect(
      readFileSync(join(root, 'CLAUDE.md'), 'utf8'),
      'Write project rules in AGENTS.md. CLAUDE.md holds only the line `@AGENTS.md`.',
    ).toBe('@AGENTS.md\n')
  })

  it('has no second copy under .claude/', () => {
    expect(
      existsSync(join(root, '.claude', 'CLAUDE.md')),
      'Move the rules of .claude/CLAUDE.md into AGENTS.md, then delete .claude/CLAUDE.md.',
    ).toBe(false)
  })
})

describe('AGENTS.md', () => {
  // A missing or empty import target gives no error. The engine then loads no project rules.
  it('is a regular file with content, so the import of CLAUDE.md finds the rules', () => {
    const path = join(root, 'AGENTS.md')
    expect(lstatSync(path).isFile()).toBe(true)
    expect(readFileSync(path, 'utf8').trim()).not.toBe('')
  })
})
