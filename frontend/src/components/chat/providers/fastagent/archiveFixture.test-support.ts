import { FASTAGENT_ARCHIVE } from '~/generated/contracts/fastagent-protocol'

export const FAST_AGENT_ARCHIVE_SPAN = 'root-archive-call'
export const FAST_AGENT_ARCHIVE_REQUEST = {
  method: 'tools/call',
  params: {
    name: 'bash',
    arguments: { command: 'printf native-output' },
  },
}

/** Supply the stored provenance of one unchanged native source element. */
export function fastAgentArchiveFixture(field: string, changes: Record<string, unknown> = {}) {
  const source: Record<string, unknown> = {
    [FASTAGENT_ARCHIVE.FormatKey]: FASTAGENT_ARCHIVE.Format,
    [FASTAGENT_ARCHIVE.CaptureID]: 'capture-1',
    [FASTAGENT_ARCHIVE.SessionID]: 'native-session',
    [FASTAGENT_ARCHIVE.Actor]: 'agent',
    [FASTAGENT_ARCHIVE.HistoryFile]: 'history_agent.json',
    [FASTAGENT_ARCHIVE.MessageIndex]: 0,
    [FASTAGENT_ARCHIVE.Role]: field === FASTAGENT_ARCHIVE.ToolResults
      ? FASTAGENT_ARCHIVE.UserRole
      : FASTAGENT_ARCHIVE.AssistantRole,
    [FASTAGENT_ARCHIVE.Field]: field,
    [FASTAGENT_ARCHIVE.Index]: 0,
    [FASTAGENT_ARCHIVE.CallID]: 'model-call',
    [FASTAGENT_ARCHIVE.ToolName]: 'bash',
    [FASTAGENT_ARCHIVE.SpanID]: FAST_AGENT_ARCHIVE_SPAN,
    [FASTAGENT_ARCHIVE.Request]: FAST_AGENT_ARCHIVE_REQUEST,
    ...changes,
  }
  return {
    spanId: FAST_AGENT_ARCHIVE_SPAN,
    spanType: 'bash',
    agentSessionId: 'native-session',
    supplementalContent: { [FASTAGENT_ARCHIVE.Namespace]: source },
  }
}
