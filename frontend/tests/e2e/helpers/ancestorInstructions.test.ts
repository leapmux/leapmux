import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ANCESTOR_INSTRUCTION_FILES, ANCESTOR_INSTRUCTION_SENTINEL, holdsAncestorInstructions, writeAncestorInstructionSentinels } from './ancestorInstructions'

let runRoot: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runRoot = mkdtempSync(join(scratch, 'ancestor-instructions-test-'))
})

afterEach(() => rmSync(runRoot, { recursive: true, force: true }))

describe('writeAncestorInstructionSentinels', () => {
  it('writes each instruction file with the sentinel, its parent directories included', () => {
    writeAncestorInstructionSentinels(runRoot)
    for (const file of ANCESTOR_INSTRUCTION_FILES)
      expect(readFileSync(join(runRoot, file), 'utf8'), file).toContain(ANCESTOR_INSTRUCTION_SENTINEL)
  })

  it('refuses to replace a file that the run root already holds', () => {
    writeFileSync(join(runRoot, 'AGENTS.md'), 'an earlier file\n')
    expect(() => writeAncestorInstructionSentinels(runRoot)).toThrow(expect.objectContaining({ code: 'EEXIST' }))
    expect(readFileSync(join(runRoot, 'AGENTS.md'), 'utf8')).toBe('an earlier file\n')
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
