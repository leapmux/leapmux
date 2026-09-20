import type { ProviderPlugin } from '../capabilities'
import { CLAUDE_DEFAULT_MODE, CLAUDE_MODE } from '~/generated/contracts/claude-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyClaudeCodeMessage } from './classification'
import { claudeCompactionBoundary, claudeNotificationEntry } from './extractors/notification'
import { claudeResultDivider } from './extractors/resultDivider'
import { claudeExtractRow } from './extractors/row'
import { claudeControls } from './pluginControls'
import { claudeRateLimitsFromMessage } from './rateLimits'
import { claudeContextUsageFromMessage } from './sessionMetadata'
import { claudeRelatedMessages, claudeSpanRole } from './spanRole'

// Claude reserves ~16.5% of the context window as an autocompact buffer, so the
// context-usage percentage is measured against the remaining usable capacity.
const CLAUDE_AUTOCOMPACT_BUFFER_PCT = 16.5

const claudeCodePlugin: ProviderPlugin = {
  transcript: {
    classify: classifyClaudeCodeMessage,
    spanRole: claudeSpanRole,
    relatedMessages: claudeRelatedMessages,
    extractRow: claudeExtractRow,
    notificationEntry: claudeNotificationEntry,
    extractDivider: claudeResultDivider,
  },
  controls: claudeControls,
  session: {
    rateLimitsFromMessage: claudeRateLimitsFromMessage,
    contextUsageFromMessage: claudeContextUsageFromMessage,
    compactionBoundaryFromMessage: claudeCompactionBoundary,
    contextBufferPct: CLAUDE_AUTOCOMPACT_BUFFER_PCT,
  },
  configuration: {
    attachments: {
      text: true,
      image: true,
      pdf: true,
      binary: false,
    },
    planMode: buildPlanMode('permissionMode', CLAUDE_MODE.Plan, CLAUDE_DEFAULT_MODE),
    // The trigger's mode segment shows the permission mode (which is also Claude's
    // plan axis, so it naturally reads "Plan Mode" when in plan).
    triggerModeGroupKey: 'permissionMode',
  },
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)
