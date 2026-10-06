import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

/**
 * The guard against instruction files above the working directory of a native agent.
 *
 * Many agent CLIs read instruction files from each directory above their working directory: Claude Code reads
 * `CLAUDE.md` up to the root of the file system, and other CLIs read `AGENTS.md` or a file of their own up to the
 * root of the git repository around them. A test must control everything that an agent reads, so an agent may read
 * no instruction file above its working directory. The launcher writes a sentinel file of each known name into the
 * run root, which is above every working directory of the run, and the mock model server refuses a request that
 * holds the sentinel. A provider that reads above its working directory therefore fails its test, and it needs either
 * a working directory that is the root of a git repository of its own (`gitRepositoryWorkingDir`) or a setting that
 * excludes the files above it.
 */

/**
 * The text that each sentinel file holds. It is one word of capital letters and digits, because a provider can escape
 * punctuation when it quotes a file into a request.
 */
export const ANCESTOR_INSTRUCTION_SENTINEL = 'LEAPMUXE2EANCESTORINSTRUCTIONS'

/**
 * The instruction files that the agent CLIs read from a directory above their working directory, as paths relative to
 * that directory. A name that a provider reads and that this list lacks escapes the guard.
 */
export const ANCESTOR_INSTRUCTION_FILES = [
  'AGENTS.md',
  'AGENT.md',
  'AGENTS.override.md',
  'CLAUDE.md',
  'CLAUDE.local.md',
  '.claude/CLAUDE.md',
  'GEMINI.md',
  'QWEN.md',
  'CODEBUDDY.md',
  'CONVENTIONS.md',
  '.cursorrules',
  '.clinerules',
  '.goosehints',
  '.github/copilot-instructions.md',
  '.junie/guidelines.md',
] as const

/** The text of each sentinel file: the sentinel, and a sentence for a reader who finds the file. */
const SENTINEL_TEXT = `${ANCESTOR_INSTRUCTION_SENTINEL}\n\nThe E2E launcher wrote this file into the run root. An agent that reads it reads instruction files above its working directory.\n`

/** Write each sentinel instruction file into `runRoot`. */
export function writeAncestorInstructionSentinels(runRoot: string): void {
  for (const file of ANCESTOR_INSTRUCTION_FILES) {
    const path = join(runRoot, file)
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, SENTINEL_TEXT, { flag: 'wx' })
  }
}

/** Whether a model request body holds the text of a sentinel instruction file. */
export function holdsAncestorInstructions(body: unknown): boolean {
  return JSON.stringify(body ?? null).includes(ANCESTOR_INSTRUCTION_SENTINEL)
}
