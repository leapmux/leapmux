import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'
import { cursorControlResponseSummary } from './controlResponse'
import { cursorExtractControl } from './extractControl'
import { cursorToolCallAdapter } from './extractors/toolCall'
import { cursorControlActionsFor, cursorQuestionHandling } from './pluginControls'

registerACPProvider({
  provider: AgentProvider.CURSOR,
  toolCallAdapter: cursorToolCallAdapter,
  defaultPermissionMode: 'agent',
  controlResponseDisplay: cursorControlResponseSummary,
  extractControl: cursorExtractControl,
  // Cursor answers its create-plan itself: the verdict its worker transforms is
  // neither a permission decision nor the shared plan approval's. Every Agent
  // Client Protocol permission beside it takes the shared decision row.
  controlActionsFor: cursorControlActionsFor,
  planValue: 'plan',
  questionHandling: cursorQuestionHandling,
})
