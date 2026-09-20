import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyPiMessage } from './classification'
import { piCompactionBoundary, piNotificationEntry } from './extractors/notification'
import { piResultDivider } from './extractors/resultDivider'
import { piExtractRow } from './extractors/row'
import { piControls } from './pluginControls'
import { resolvePiMessage } from './resolveMessage'
import { piValidateResumeHandle } from './resumeHandle'
import { piContextUsageFromMessage } from './sessionMetadata'
import { piRelatedMessages, piSpanRole } from './spanRole'

const piPlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolvePiMessage,
    spanRole: piSpanRole,
    relatedMessages: piRelatedMessages,
    classify: classifyPiMessage,
    extractRow: piExtractRow,
    // The sole Pi notification seam: consulted by the shared thread reader for each
    // message (a standalone notification or one entry of a consolidated wrapper),
    // so a multi-event thread yields every entry, not just the first.
    notificationEntry: piNotificationEntry,
    extractDivider: piResultDivider,
  },
  controls: piControls,
  session: {
    contextUsageFromMessage: piContextUsageFromMessage,
    compactionBoundaryFromMessage: piCompactionBoundary,
    // Pi's agentSessionId is a .jsonl session-file path, so the UI shortens it for
    // display and labels the copy action "session file path".
    sessionIdIsFilePath: true,
    validateResumeHandle: piValidateResumeHandle,
  },
  configuration: {
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
  },
}

registerProvider(AgentProvider.PI, piPlugin)
