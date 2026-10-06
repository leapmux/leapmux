import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

export interface ClaudeEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The origin of the mock, which serves the Anthropic Messages protocol. */
  modelURL: string
  modelKey: string
}

/** Point Claude Code at the mock, with its configuration in the isolated HOME and its background traffic off. */
export function createClaudeEnvironment(options: ClaudeEnvironmentOptions): Record<string, string> {
  const configDir = join(options.homeDir, '.claude')
  mkdirSync(configDir, { recursive: true })
  return {
    ANTHROPIC_API_KEY: options.modelKey,
    ANTHROPIC_BASE_URL: options.modelURL,
    CLAUDE_CONFIG_DIR: configDir,
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
    DISABLE_TELEMETRY: '1',
  }
}
