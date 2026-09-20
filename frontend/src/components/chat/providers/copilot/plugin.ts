import type { ProviderPlugin } from '../capabilities'
import { COPILOT_MODE, COPILOT_OPTION } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyCopilotMessage, copilotResultDivider } from './classification'
import { copilotCompactionBoundary, copilotNotificationEntry } from './extractors/notification'
import { copilotExtractRow } from './extractors/row'
import { copilotControls } from './pluginControls'
import { copilotContextUsage } from './sessionMetadata'
import { copilotRelatedMessages, copilotSpanRole } from './spanRole'

const copilotPlugin: ProviderPlugin = {
  transcript: {
    classify: classifyCopilotMessage,
    extractRow: copilotExtractRow,
    spanRole: copilotSpanRole,
    relatedMessages: copilotRelatedMessages,
    extractDivider: copilotResultDivider,
    notificationEntry: copilotNotificationEntry,
  },
  controls: copilotControls,
  session: {
    contextUsageFromMessage: copilotContextUsage,
    compactionBoundaryFromMessage: copilotCompactionBoundary,
  },
  configuration: {
    attachments: { text: true, image: true, pdf: true, binary: true },
    // Copilot's plan axis is its SESSION mode, beside the permission mode its presets
    // drive. The two are independent, which is why the mode chip reads the session-mode
    // group and the presets write the permission-mode one.
    triggerModeGroupKey: COPILOT_OPTION.SessionMode,
    planMode: buildPlanMode(COPILOT_OPTION.SessionMode, COPILOT_MODE.Plan, COPILOT_MODE.Interactive),
  },
}

registerProvider(AgentProvider.GITHUB_COPILOT, copilotPlugin)
