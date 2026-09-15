import type { AgentRequestSource } from './AgentRequestMessage'
import type { AgentResultSource } from './agentResult'
import type { CommandResultEntry, CommandResultSource } from './commandResult'
import type { DirectoryResultSource } from './directoryResult'
import type { FileEditDiffSource } from './fileEditDiff'
import type { McpToolCallSource } from './mcpToolCall'
import type { ReadFileResultSource } from './readFileResult'
import type { SearchResultSource } from './searchResult'
import type { StatusResultSource } from './statusResult'
import type { ToolKind } from './toolKind'
import type { ToolMetadataItem } from './ToolMetadata'
import type { WebFetchResultSource } from './webFetchResult'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { TodoItem } from '~/stores/chatTodos'
import { pluralize } from '~/lib/plural'

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
    | { type: 'status', source: StatusResultSource }
    | {
      type: 'todo'
      items: TodoItem[]
      /** What the row says in place of an empty list. The shared default is a clear. */
      emptyText?: string
      /** A note about the listed task, drawn as Markdown below the list. */
      description?: string
    }
    | { type: 'markdown', text: string }
    | { type: 'text' }

export interface ToolPresentation {
  kind: ToolKind
  title: string
  label?: string
  agentRequest?: AgentRequestSource
  input: Record<string, unknown>
  inputText?: string
  /** File operations from a patch request. They do not establish that the changes occurred. */
  requestedChanges?: FileEditDiffSource[]
  output: string
  body: ToolBodySource
  /**
   * The language the row highlights `input.command` with. Omit for `bash`, which is
   * what every provider but one runs. Pi alone reports a PowerShell tool.
   */
  commandLanguage?: 'bash' | 'powershell'
  /**
   * The provider kept only part of what the call produced, and no body below states
   * it. A body that carries its own `truncated` field -- a command, a search, a
   * directory -- states it there instead, so the row never draws the notice twice.
   */
  truncated?: boolean
  /** Rich content that accompanies a specialized result body. */
  additionalContent?: McpToolCallSource
  metadata?: ToolMetadataItem[]
  unresolvedTerminals: string[]
}

/** Provider extraction supplies identity and state separately from the display model. */
export interface ToolMessageSource {
  id: string
  role: 'request' | 'update' | 'result'
  status: string
  presentation: ToolPresentation
  images: ImageResultSource[]
}

/**
 * The to-do body every provider's checklist tool draws, with the list size in its
 * header.
 *
 * The header must state the SIZE rather than the raw tool name, which says nothing the
 * list below does not already show. An empty list is a clear, and it says so. Three
 * providers spelled these three fields separately, and one of them drew a bare tool
 * name for a while because it missed the empty case.
 */
export function todoToolBody(items: TodoItem[]): Pick<ToolPresentation, 'kind' | 'title' | 'body'> {
  return {
    kind: 'todo',
    title: items.length ? pluralize(items.length, 'task') : 'To-do list cleared',
    body: { type: 'todo', items },
  }
}
