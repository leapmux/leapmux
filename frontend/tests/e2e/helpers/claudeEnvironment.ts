import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export interface ClaudeEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The origin of the mock, which serves the Anthropic Messages protocol. */
  modelURL: string
  modelKey: string
}

/**
 * The instruction files that the agent may not read: every memory file of Claude Code and every `AGENTS.md`, wherever
 * each one is.
 *
 * Claude Code reads `CLAUDE.md` and its relatives from each directory above its working directory, up to the root of
 * the file system, and a git repository does not stop it. Its built-in plugin `cc-plugin-agents-md` reads `AGENTS.md`
 * and `.claude/AGENTS.md` from the same directories when no `CLAUDE.md` applies, which is so whenever this setting
 * excludes them all. The plugin reads each file through the memory loader, as the `Project` type, so the setting
 * excludes it too. Claude Code 2.1.289 shows this. The sentinel files of the run root
 * (`./ancestorInstructions.ts`) are above every working directory of a spec, and a spec that opens Claude Code in the
 * checkout or under a home would read the developer's own instructions. No spec relies on one of these files, so the
 * agent reads none. Claude Code matches each pattern against the absolute path with picomatch.
 */
const CLAUDE_MEMORY_EXCLUDES = ['**/CLAUDE.md', '**/CLAUDE.local.md', '**/.claude/CLAUDE.md', '**/.claude/rules/**', '**/AGENTS.md', '**/.claude/AGENTS.md']

/** Point Claude Code at the mock, with its configuration in the isolated HOME and its background traffic off. */
export function createClaudeEnvironment(options: ClaudeEnvironmentOptions): Record<string, string> {
  const configDir = join(options.homeDir, '.claude')
  mkdirSync(configDir, { recursive: true })
  writeFileSync(join(configDir, 'settings.json'), `${JSON.stringify({ claudeMdExcludes: CLAUDE_MEMORY_EXCLUDES })}\n`)
  return {
    ANTHROPIC_API_KEY: options.modelKey,
    ANTHROPIC_BASE_URL: options.modelURL,
    CLAUDE_CONFIG_DIR: configDir,
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
    DISABLE_TELEMETRY: '1',
  }
}
