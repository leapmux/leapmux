import { GEMINI_MODE } from '~/generated/contracts/gemini-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createACPProvider } from '../acp/registerACPProvider'
import { registerProvider } from '../registry'
import { geminiExtractControl } from './extractControl'
import { geminiToolCallAdapter } from './extractors/toolCall'
import { geminiPermissionPresets } from './permissionPresets'
import { composeGeminiTranscript } from './transcript'

const base = createACPProvider({
  toolCallAdapter: geminiToolCallAdapter,
  extractControl: geminiExtractControl,
  defaultPermissionMode: GEMINI_MODE.Default,
  planValue: GEMINI_MODE.Plan,
  permissionPresets: geminiPermissionPresets,
  attachments: { text: true, image: true, pdf: true, binary: true },
})

registerProvider(AgentProvider.GEMINI_CLI, { ...base, transcript: composeGeminiTranscript(base.transcript) })
