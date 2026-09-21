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

/** One operation that an execute request performs. */
export type CommandAction
  = | { kind: 'read', command: string, name: string, path: string }
    | { kind: 'list', command: string, path?: string }
    | { kind: 'search', command: string, query?: string, path?: string }
    | { kind: 'unknown', command: string }

export interface ExecuteRequest {
  command: string
  language?: CommandLanguage
  description?: string
  cwd?: string
  /** The provider's best-effort breakdown of a compound command. */
  actions?: CommandAction[]
  /** The provider's process identifier. It can be opaque and is not necessarily numeric. */
  processId?: string
}
export interface ExecuteResult { commands: CommandResult[], unresolvedTerminals: string[] }
