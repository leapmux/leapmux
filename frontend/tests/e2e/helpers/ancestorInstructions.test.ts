import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ANCESTOR_INSTRUCTION_FILES, ANCESTOR_INSTRUCTION_SENTINEL, holdsAncestorInstructions, refusedInstructionFiles, writeAncestorInstructionSentinels } from './ancestorInstructions'

let runRoot: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runRoot = mkdtempSync(join(scratch, 'ancestor-instructions-test-'))
})

afterEach(() => rmSync(runRoot, { recursive: true, force: true }))

/** The text of the sentinel file at `path` after `writeAncestorInstructionSentinels` ran. */
function sentinelText(path: string): string {
  return readFileSync(join(runRoot, path), 'utf8')
}

describe('writeAncestorInstructionSentinels', () => {
  it('writes each instruction file with the sentinel, its parent directories included', () => {
    writeAncestorInstructionSentinels(runRoot)
    for (const { path } of ANCESTOR_INSTRUCTION_FILES)
      expect(sentinelText(path), path).toContain(ANCESTOR_INSTRUCTION_SENTINEL)
  })

  it('refuses to replace a file that the run root already holds', () => {
    writeFileSync(join(runRoot, 'AGENTS.md'), 'an earlier file\n')
    expect(() => writeAncestorInstructionSentinels(runRoot)).toThrow(expect.objectContaining({ code: 'EEXIST' }))
    expect(readFileSync(join(runRoot, 'AGENTS.md'), 'utf8')).toBe('an earlier file\n')
  })

  // Cursor drops a .mdc rule without a frontmatter block, and Oh My Pi drops a rule that states no `alwaysApply`.
  // GitHub Copilot applies an .instructions.md file to the paths in `applyTo`.
  it('opens each rule with a frontmatter block that applies it to every request', () => {
    writeAncestorInstructionSentinels(runRoot)
    const rules = ANCESTOR_INSTRUCTION_FILES.filter(file => file.form === 'rule')
    expect(rules.map(file => file.path)).toEqual(expect.arrayContaining(['.clinerules', `.cursor/rules/${ANCESTOR_INSTRUCTION_SENTINEL}.mdc`]))
    for (const { path } of rules)
      expect(sentinelText(path), path).toMatch(/^---\nalwaysApply: true\napplyTo: "\*\*"\n---\n\n/)
  })

  // Codewhale renders the `authority` list of a constitution into its prompt, and ignores a file that does not parse.
  it('writes each constitution as JSON whose authority list holds the sentinel', () => {
    writeAncestorInstructionSentinels(runRoot)
    const constitutions = ANCESTOR_INSTRUCTION_FILES.filter(file => file.form === 'constitution')
    expect(constitutions.map(file => file.path)).toEqual(['.codewhale/constitution.json'])
    for (const { path } of constitutions)
      expect(JSON.parse(sentinelText(path)).authority).toContain(ANCESTOR_INSTRUCTION_SENTINEL)
  })

  it('writes each document as the sentinel, with no frontmatter', () => {
    writeAncestorInstructionSentinels(runRoot)
    for (const { path } of ANCESTOR_INSTRUCTION_FILES.filter(file => file.form === 'document'))
      expect(sentinelText(path), path).toMatch(new RegExp(`^${ANCESTOR_INSTRUCTION_SENTINEL}\n`))
  })
})

describe('ANCESTOR_INSTRUCTION_FILES', () => {
  // A case-insensitive file system holds `AGENTS.md` and `agents.md` as one file, and the second write would fail.
  it('holds no two paths that differ only in case', () => {
    const paths = ANCESTOR_INSTRUCTION_FILES.map(file => file.path.toLowerCase())
    expect(new Set(paths).size).toBe(paths.length)
  })

  it('holds only relative paths that stay inside the run root', () => {
    for (const { path } of ANCESTOR_INSTRUCTION_FILES) {
      expect(path, path).not.toMatch(/^\//)
      expect(path.split('/'), path).not.toContain('..')
    }
  })

  // Each of these names escaped the guard once: a provider read it above its working directory, and the list lacked it.
  it.each([
    ['Claude Code', '.claude/AGENTS.md'],
    ['Claude Code', `.claude/rules/${ANCESTOR_INSTRUCTION_SENTINEL}.md`],
    ['CodeBuddy Code', '.codebuddy/CODEBUDDY.md'],
    ['Oh My Pi', '.omp/AGENTS.md'],
    ['Oh My Pi', '.agents/AGENTS.md'],
    ['GitHub Copilot', `.github/instructions/${ANCESTOR_INSTRUCTION_SENTINEL}.instructions.md`],
    ['Cursor', `.cursor/rules/${ANCESTOR_INSTRUCTION_SENTINEL}.mdc`],
    ['Codewhale', '.codewhale/constitution.json'],
    ['OpenCode', 'CONTEXT.md'],
    ['DeepSeek Harness', 'AGENTS.local.md'],
    ['Reasonix', 'REASONIX.md'],
    ['Factory Droid', 'DESIGN.md'],
  ])('holds a name that %s reads above its working directory: %s', (_provider, path) => {
    expect(ANCESTOR_INSTRUCTION_FILES.map(file => file.path)).toContain(path)
  })
})

describe('ANCESTOR_INSTRUCTION_SENTINEL', () => {
  // A provider can escape Markdown punctuation when it quotes a file, so only letters and digits survive every quoting.
  it('is one word of capital letters and digits', () => {
    expect(ANCESTOR_INSTRUCTION_SENTINEL).toMatch(/^[A-Z0-9]+$/)
  })
})

describe('holdsAncestorInstructions', () => {
  it('finds the sentinel in a nested request body', () => {
    expect(holdsAncestorInstructions({ messages: [{ role: 'system', content: [{ type: 'text', text: `Contents of /run/CLAUDE.md:\n${ANCESTOR_INSTRUCTION_SENTINEL}` }] }] })).toBe(true)
  })

  it('finds the sentinel in a plain text body', () => {
    expect(holdsAncestorInstructions(`prefix ${ANCESTOR_INSTRUCTION_SENTINEL} suffix`)).toBe(true)
  })

  it.each([
    ['an ordinary request', { messages: [{ role: 'user', content: 'Reply once.' }] }],
    ['an empty body', {}],
    ['an absent body', undefined],
    ['a null body', null],
  ])('finds no sentinel in %s', (_label, body) => {
    expect(holdsAncestorInstructions(body)).toBe(false)
  })
})

describe('refusedInstructionFiles', () => {
  const sentinelFile = (path: string) => ({ path, content: `${ANCESTOR_INSTRUCTION_SENTINEL}\n` })

  it('refuses each file outside the run root, for both policies', () => {
    const outside = [{ path: join(dirname(runRoot), 'AGENTS.md'), content: 'A file above the run root.' }, { path: '/AGENTS.md', content: 'The root of the file system.' }]
    for (const policy of ['refuse', 'known-escape'] as const)
      expect(refusedInstructionFiles(outside, runRoot, policy), policy).toEqual(outside)
  })

  it('keeps a file of a working directory inside the run root, for both policies', () => {
    const project = [{ path: join(runRoot, '1', 'work', 'AGENTS.md'), content: 'Project guidance.' }]
    for (const policy of ['refuse', 'known-escape'] as const)
      expect(refusedInstructionFiles(project, runRoot, policy), policy).toEqual([])
  })

  it('refuses a sentinel file of the run root for a provider that reads none, and lets it through as a known escape', () => {
    const sentinel = sentinelFile(join(runRoot, 'AGENTS.md'))
    expect(refusedInstructionFiles([sentinel], runRoot, 'refuse')).toEqual([sentinel])
    expect(refusedInstructionFiles([sentinel], runRoot, 'known-escape')).toEqual([])
  })

  it('refuses a relative path, which no caller can place', () => {
    const relative = [{ path: 'AGENTS.md', content: 'Somewhere.' }, { path: '', content: 'Nowhere.' }]
    expect(refusedInstructionFiles(relative, runRoot, 'known-escape')).toEqual(relative)
  })

  it('checks the sentinel alone without a run root', () => {
    const anywhere = { path: '/AGENTS.md', content: 'Plain guidance.' }
    const sentinel = sentinelFile('/elsewhere/AGENTS.md')
    expect(refusedInstructionFiles([anywhere, sentinel], undefined, 'refuse')).toEqual([sentinel])
    expect(refusedInstructionFiles([anywhere, sentinel], undefined, 'known-escape')).toEqual([])
  })

  it('refuses nothing for an empty list', () => {
    expect(refusedInstructionFiles([], runRoot, 'refuse')).toEqual([])
  })
})
