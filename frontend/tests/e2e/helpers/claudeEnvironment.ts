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
 * The Claude Code memory files that the agent may not read: every one, wherever it is.
 *
 * Claude Code reads `CLAUDE.md` and its relatives from each directory above its working directory, up to the root of
 * the file system, and a git repository does not stop it. A spec that opens Claude Code in the checkout, or anywhere
 * under a home, would then read the developer's own instructions. No spec relies on a memory file, so the agent reads
 * none. Claude Code matches each pattern against the absolute path with picomatch.
 */
const CLAUDE_MEMORY_EXCLUDES = ['**/CLAUDE.md', '**/CLAUDE.local.md', '**/.claude/CLAUDE.md', '**/.claude/rules/**']

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
