import type { AgentRequestSource } from './AgentRequestMessage'
import type { AgentResultSource } from './agentResult'
import type { CommandResultEntry, CommandResultSource } from './commandResult'
import type { DirectoryResultSource } from './directoryResult'
import type { FileEditDiffSource } from './fileEditDiff'
import type { McpToolCallSource } from './mcpToolCall'
import type { ReadFileResultSource } from './readFileResult'
import type { SearchResultSource } from './searchResult'
import type { WebFetchResultSource } from './webFetchResult'
import type { TodoItem } from '~/stores/chatTodos'

/** Shared components render these sources after the provider extracts its fields. */
export type ToolBodySource
  = | { type: 'agent', source: AgentResultSource }
    | { type: 'command', source: CommandResultSource }
    | { type: 'commands', entries: CommandResultEntry[] }
    | { type: 'directory', source: DirectoryResultSource }
    | { type: 'diff', sources: FileEditDiffSource[] }
    | { type: 'read', source: ReadFileResultSource }
    | { type: 'search', source: SearchResultSource }
    | { type: 'fetch', source: WebFetchResultSource }
    | { type: 'mcp', source: McpToolCallSource }
    | { type: 'todo', items: TodoItem[] }
    | { type: 'markdown', text: string }
    | { type: 'text' }

export interface ToolPresentation {
  kind: string
  title: string
  label?: string
  agentRequest?: AgentRequestSource
  input: Record<string, unknown>
  inputText?: string
  /** File operations from a patch request. They do not establish that the changes occurred. */
  requestedChanges?: FileEditDiffSource[]
  output: string
  body: ToolBodySource
  unresolvedTerminals: string[]
}
