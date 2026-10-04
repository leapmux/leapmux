import type { ProviderPlugin } from '../capabilities'
import { buildPlanMode } from '~/components/chat/settingsGroups'
import { CODEX_OPTION, CODEX_OPTION_DEFAULT } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyCodexMessage } from './classification'
import { codexCompactionBoundary, codexNotificationEntry } from './extractors/notification'
import { codexResultDivider } from './extractors/resultDivider'
import { codexExtractRow } from './extractors/row'
import { codexControls } from './pluginControls'
import { codexRateLimitsFromMessage } from './rateLimits'
import { resolveCodexMessage } from './resolveMessage'
import { codexContextUsageFromNotification } from './sessionMetadata'
import { codexRelatedMessages, codexSpanRole } from './spanRole'

const codexPlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveCodexMessage,
    spanRole: codexSpanRole,
    relatedMessages: codexRelatedMessages,
    classify: classifyCodexMessage,
    extractRow: codexExtractRow,
    extractDivider: codexResultDivider,
    notificationEntry: codexNotificationEntry,
  },
  controls: codexControls,
  session: {
    rateLimitsFromMessage: codexRateLimitsFromMessage,
    contextUsageFromMessage: codexContextUsageFromNotification,
    compactionBoundaryFromMessage: codexCompactionBoundary,
  },
  configuration: {
    // Seed a new Codex agent with its default collaboration mode.
    defaultProviderOptions: { [CODEX_OPTION.CollaborationMode]: CODEX_OPTION_DEFAULT.CollaborationMode },
    // Multi-Agent V2 rejects direct app-server input for spawned child threads.
    // The child tab is a read-only transcript.
    supportsSubagentSend: false,
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
    planMode: buildPlanMode(CODEX_OPTION.CollaborationMode, 'plan', CODEX_OPTION_DEFAULT.CollaborationMode),
    // The trigger's mode segment shows the "Workflow" (collaboration_mode) group --
    // Codex's mode axis -- not the approval policy. It reads "Plan Mode" when the
    // workflow sits at its plan value.
    triggerModeGroupKey: CODEX_OPTION.CollaborationMode,
  },
}

registerProvider(AgentProvider.CODEX, codexPlugin)
