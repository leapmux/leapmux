import type { CommandResult } from '../commandResult'

/**
 * The language an execute call's command is written in, when the provider states one.
 *
 * The set is CLOSED because the highlighter switches on it: a word outside it reaches
 * no grammar, so the body draws unhighlighted with nothing to say why. Three modules
 * spelled the same four words, and a fifth language added to one of them left the
 * other two narrow.
 */
export type CommandLanguage = 'bash' | 'powershell' | 'javascript' | 'sql'

export interface ExecuteRequest {
  command: string
  language?: CommandLanguage
  description?: string
  cwd?: string
}
export interface ExecuteResult { commands: CommandResult[], unresolvedTerminals: string[] }
