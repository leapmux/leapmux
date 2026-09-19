import type { TranscriptFrame } from '~/test-support/messageFactory'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

const structured: TranscriptFrame = {
  id: 'structured',
  provider: AgentProvider.CODEX,
  content: {},
}
const captured: TranscriptFrame = {
  id: 'captured',
  provider: AgentProvider.CODEX,
  rawContent: new Uint8Array(),
}

// @ts-expect-error A transcript frame must state one payload source.
const missing: TranscriptFrame = { id: 'missing', provider: AgentProvider.CODEX }
// @ts-expect-error A transcript frame cannot state both payload sources.
const duplicate: TranscriptFrame = { id: 'duplicate', provider: AgentProvider.CODEX, content: {}, rawContent: new Uint8Array() }

void [structured, captured, missing, duplicate]
