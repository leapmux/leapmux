import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { isInsideDirectory } from './runRoot'

/**
 * The guard against instruction files above the working directory of a native agent.
 *
 * Many agent CLIs read instruction files from each directory above their working directory: Claude Code, CodeBuddy and
 * Pi read up to the root of the file system, and other CLIs read up to the root of the git repository around them. A
 * test must control everything that an agent reads, so an agent may read no instruction file above its working
 * directory. The launcher writes a sentinel file of each known name into the run root (`./runRoot.ts`), which is above
 * every working directory of the run, and the mock model server refuses a request that holds the sentinel. A provider
 * that reads above its working directory therefore fails its test, and it needs either a working directory that is the
 * root of a git repository of its own (`gitRepositoryWorkingDir`) or a setting or flag that turns off the files above
 * it.
 *
 * The list holds each name that one of the providers reads above its working directory, a name that another name of
 * the same directory shadows included: a setting that excludes the first name by its name would expose the next one.
 * It holds no case variant, such as `Agents.md` beside `AGENTS.md`. A case-insensitive file system holds the two as
 * one file, and each provider that reads a case variant reads the listed spelling from the same directory too.
 *
 * Cursor and Amp send their instruction files to their own services, not in a model request body, so the body check
 * cannot see them:
 *
 * - Cursor states its rules, an ancestor AGENTS.md and CLAUDE.md included, in its answer to the request context query
 *   of each turn (`cursorSurface.ts`).
 * - Amp sends its guidance files in an `executor_guidance_snapshot` frame (`ampSurface.ts`).
 *
 * Both read the instruction files of each directory up to the root of the file system, and no setting stops Cursor.
 * Each of the two surfaces therefore checks the files themselves ({@link refusedInstructionFiles}), and the mock counts
 * a refusal there as it counts a refused request body. A file outside the run root refuses its request for both
 * providers. The sentinel files of the run root stay a known escape of Cursor: Cursor reads them, and the check lets
 * them through. Amp reads none, because its environment ignores each file directly in a run root
 * (`AMP_IGNORE_GUIDANCE_FILES` in `ampEnvironment.ts`), so a guidance file of Amp that holds the sentinel refuses its
 * request.
 *
 * The guard holds no file in the directories above the run root, such as `/tmp`, which no test owns. A provider whose
 * request body quotes such a file passes the body check. Only the file check of Cursor and Amp sees such a file.
 */

/**
 * The text that each sentinel file holds. It is one word of capital letters and digits, because a provider can escape
 * punctuation when it quotes a file into a request.
 */
export const ANCESTOR_INSTRUCTION_SENTINEL = 'LEAPMUXE2EANCESTORINSTRUCTIONS'

/**
 * How a sentinel file states the sentinel, so that each provider that reads the file passes it on to the model.
 *
 * - `document`: the sentinel and a sentence for a reader, as plain Markdown. A provider inlines a document whole.
 * - `rule`: the same text after a frontmatter block that applies the rule to every request. Cursor drops a `.mdc` rule
 *   that has no frontmatter block, and Oh My Pi drops a rule that states no `alwaysApply`, no description and no
 *   condition. GitHub Copilot applies an `.instructions.md` file to the paths in `applyTo`.
 * - `constitution`: a Codewhale repository constitution, which is JSON. Codewhale renders its `authority` list into the
 *   prompt, and ignores a file that does not parse.
 */
export type AncestorInstructionForm = 'document' | 'rule' | 'constitution'

/** One sentinel file of the run root. */
export interface AncestorInstructionFile {
  /** The path of the file, relative to the directory that holds it. */
  readonly path: string
  readonly form: AncestorInstructionForm
}

/**
 * The name of each sentinel file in a directory of rules. A provider that lists a rule by its path or its name, rather
 * than by its text, then still sends the sentinel.
 */
const RULE_FILE = ANCESTOR_INSTRUCTION_SENTINEL

/**
 * The instruction files that the agent CLIs read from a directory above their working directory. A name that a provider
 * reads and that this list lacks escapes the guard.
 *
 * The comment of each later entry states the providers that read it above the working directory, with the version that
 * shows it and the place in that version. "Up to the git root" means up to the first directory above the working
 * directory that holds `.git`, a directory that each of those providers finds by its own lookup.
 */
export const ANCESTOR_INSTRUCTION_FILES: readonly AncestorInstructionFile[] = [
  { path: 'AGENTS.md', form: 'document' },
  { path: 'AGENT.md', form: 'document' },
  { path: 'AGENTS.override.md', form: 'document' },
  { path: 'CLAUDE.md', form: 'document' },
  { path: 'CLAUDE.local.md', form: 'document' },
  { path: '.claude/CLAUDE.md', form: 'document' },
  { path: 'GEMINI.md', form: 'document' },
  { path: 'QWEN.md', form: 'document' },
  { path: 'CODEBUDDY.md', form: 'document' },
  { path: 'CONVENTIONS.md', form: 'document' },
  { path: '.cursorrules', form: 'document' },
  // Oh My Pi 18.6.0 takes the nearest `.clinerules` up to the root of the file system (`discovery/cline.ts`), and
  // keeps a rule that states `alwaysApply`. Cline 3.0.68 reads it at the root of the git repository.
  { path: '.clinerules', form: 'rule' },
  { path: '.goosehints', form: 'document' },
  { path: '.github/copilot-instructions.md', form: 'document' },
  { path: '.junie/guidelines.md', form: 'document' },
  // Claude Code 2.1.289: its built-in plugin `cc-plugin-agents-md` reads both names from each directory up to the root
  // of the file system (`AGENTS_NAMES`, through `fs.ancestors`), when no CLAUDE.md applies.
  { path: '.claude/AGENTS.md', form: 'document' },
  // Claude Code 2.1.289 reads `.claude/rules/**/*.md` from each directory up to the root of the file system, and a rule
  // with no `paths` frontmatter always applies. Grok Build 1.0.46 reads `.claude/rules/*.md` up to the git root.
  { path: `.claude/rules/${RULE_FILE}.md`, form: 'document' },
  // Grok Build 1.0.46 reads these up to the git root (`xai-grok-config/src/compat.rs`).
  { path: '.claude/CLAUDE.local.md', form: 'document' },
  { path: `.grok/rules/${RULE_FILE}.md`, form: 'document' },
  { path: `.cursor/rules/${RULE_FILE}.md`, form: 'document' },
  // Cursor (cursor-agent 2026.09.28) reads `.cursor/rules/**/*.mdc` from each directory up to the root of the file
  // system (`loadRulesFromDirAndAncestors`).
  { path: `.cursor/rules/${RULE_FILE}.mdc`, form: 'rule' },
  // GitHub Copilot CLI 1.0.87 reads its instruction files from each directory up to the git root, and further up when
  // no repository holds the working directory.
  { path: `.github/instructions/${RULE_FILE}.instructions.md`, form: 'rule' },
  // CodeBuddy Code 2.160.0 reads the first of CODEBUDDY.md, CODEBUDDY.mdc, AGENTS.md and AGENTS.mdc from each directory
  // up to the root of the file system, and the first of the same names in its `.codebuddy/`
  // (`MemoryLoader.loadProjectMainFiles`).
  { path: 'CODEBUDDY.mdc', form: 'document' },
  { path: 'AGENTS.mdc', form: 'document' },
  { path: '.codebuddy/CODEBUDDY.md', form: 'document' },
  { path: '.codebuddy/CODEBUDDY.mdc', form: 'document' },
  { path: '.codebuddy/AGENTS.md', form: 'document' },
  { path: '.codebuddy/AGENTS.mdc', form: 'document' },
  // Oh My Pi 18.6.0 reads these from the nearest `.omp/` up to the git root, and up to the root of the file system when
  // no repository holds the working directory (`discovery/builtin.ts`).
  { path: '.omp/AGENTS.md', form: 'document' },
  { path: '.omp/RULES.md', form: 'document' },
  { path: '.omp/SYSTEM.md', form: 'document' },
  { path: '.omp/SYSTEM_TEMPLATE.md', form: 'document' },
  // Oh My Pi 18.6.0 reads AGENTS.md, SYSTEM.md, SYSTEM_TEMPLATE.md and `rules/` of each `.agent/` and `.agents/` up to
  // the git root, and up to the root of the file system without a repository (`discovery/agents.ts`). Factory Droid
  // 0.233.0 reads AGENTS.md, CLAUDE.md and DESIGN.md of the same directories, and of `.factory/`, up to the git root.
  { path: '.agent/AGENTS.md', form: 'document' },
  { path: '.agent/CLAUDE.md', form: 'document' },
  { path: '.agent/DESIGN.md', form: 'document' },
  { path: '.agent/SYSTEM.md', form: 'document' },
  { path: '.agent/SYSTEM_TEMPLATE.md', form: 'document' },
  { path: `.agent/rules/${RULE_FILE}.md`, form: 'rule' },
  { path: '.agents/AGENTS.md', form: 'document' },
  { path: '.agents/CLAUDE.md', form: 'document' },
  { path: '.agents/DESIGN.md', form: 'document' },
  { path: '.agents/SYSTEM.md', form: 'document' },
  { path: '.agents/SYSTEM_TEMPLATE.md', form: 'document' },
  { path: `.agents/rules/${RULE_FILE}.md`, form: 'rule' },
  { path: 'DESIGN.md', form: 'document' },
  { path: '.factory/AGENTS.md', form: 'document' },
  { path: '.factory/CLAUDE.md', form: 'document' },
  { path: '.factory/DESIGN.md', form: 'document' },
  // Codewhale 0.10.0 reads the nearest constitution up to the git root, and up to the root of the file system without
  // a repository (`load_repo_constitution_block`). Its instruction chain, the first of AGENTS.md and the two others in
  // each directory, runs from the git root down to the working directory.
  { path: '.codewhale/constitution.json', form: 'constitution' },
  { path: '.codewhale/instructions.md', form: 'document' },
  { path: '.deepseek/instructions.md', form: 'document' },
  // OpenCode 1.18.34, Kilo 7.8.3 and MiMo Code 0.1.15 read the first of AGENTS.md, CLAUDE.md and CONTEXT.md that any
  // directory up to the git root holds, and up to the root of the file system without a repository.
  { path: 'CONTEXT.md', form: 'document' },
  // DeepSeek Harness 0.2.0-rc.2, Qoder CLI 1.1.65 and Reasonix 1.38.7 read the local overlay up to the git root.
  { path: 'AGENTS.local.md', form: 'document' },
  // Reasonix 1.38.7 reads its own names up to the git root (`internal/instruction/resolver.go`).
  { path: 'REASONIX.md', form: 'document' },
  { path: 'REASONIX.local.md', form: 'document' },
  // Qoder CLI 1.1.65 reads `.qoder/rules/**/*.md` up to the git root, and a rule with no frontmatter always applies.
  { path: `.qoder/rules/${RULE_FILE}.md`, form: 'document' },
  // Qwen Code 0.24.7 reads both at the git root, which is above a working directory in a subdirectory of the repository.
  { path: '.qwen/QWEN.local.md', form: 'document' },
  { path: `.qwen/rules/${RULE_FILE}.md`, form: 'document' },
  // Kimi Code 2.1.1 reads it from each directory from the git root down to the working directory.
  { path: '.kimi-code/AGENTS.md', form: 'document' },
  // Cline 3.0.68 reads it at the root of the git repository, which is above a working directory in a subdirectory.
  { path: `.cline/rules/${RULE_FILE}.md`, form: 'document' },
]

/** The sentence after the sentinel in each sentinel file, for a reader who finds the file. */
const SENTINEL_SENTENCE = 'The E2E launcher wrote this file into the run root. An agent that reads it reads instruction files above its working directory.'

/** The text of a sentinel file of each form. */
const SENTINEL_TEXT: Readonly<Record<AncestorInstructionForm, string>> = {
  document: `${ANCESTOR_INSTRUCTION_SENTINEL}\n\n${SENTINEL_SENTENCE}\n`,
  rule: `---\nalwaysApply: true\napplyTo: "**"\n---\n\n${ANCESTOR_INSTRUCTION_SENTINEL}\n\n${SENTINEL_SENTENCE}\n`,
  constitution: `${JSON.stringify({ authority: [ANCESTOR_INSTRUCTION_SENTINEL, SENTINEL_SENTENCE] }, null, 2)}\n`,
}

/** Write each sentinel instruction file into `runRoot`. */
export function writeAncestorInstructionSentinels(runRoot: string): void {
  for (const { path, form } of ANCESTOR_INSTRUCTION_FILES) {
    const file = join(runRoot, path)
    mkdirSync(dirname(file), { recursive: true })
    writeFileSync(file, SENTINEL_TEXT[form], { flag: 'wx' })
  }
}

/** Whether a model request body holds the text of a sentinel instruction file. */
export function holdsAncestorInstructions(body: unknown): boolean {
  return JSON.stringify(body ?? null).includes(ANCESTOR_INSTRUCTION_SENTINEL)
}

/** One instruction file that a native agent sent to its own service rather than to the model. */
export interface NativeInstructionFile {
  /** The path of the file, as the agent states it. */
  readonly path: string
  readonly content: string
}

/**
 * How the file check treats the sentinel files of the run root, for a provider that sends its instruction files to its
 * own service.
 *
 * - `refuse`: the environment of the provider keeps it from the sentinel files, so a file that holds the sentinel
 *   refuses its request, as the body check refuses a request body. Amp.
 * - `known-escape`: no setting keeps the provider from the sentinel files, so the check lets each file of the run root
 *   through. Cursor.
 */
export type RunRootSentinelPolicy = 'refuse' | 'known-escape'

/**
 * The instruction files that refuse their request: each file outside `runRoot`, and with the `refuse` policy, each file
 * that holds the sentinel. A server with no run root, such as one of a unit test, checks the sentinel alone.
 */
export function refusedInstructionFiles(
  files: readonly NativeInstructionFile[],
  runRoot: string | undefined,
  policy: RunRootSentinelPolicy,
): NativeInstructionFile[] {
  return files.filter(file =>
    (runRoot !== undefined && !isInsideDirectory(file.path, runRoot))
    || (policy === 'refuse' && file.content.includes(ANCESTOR_INSTRUCTION_SENTINEL)))
}
