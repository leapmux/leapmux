import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyClineMessage } from './classification'
import { clineCompactionBoundary, clineNotificationEntry } from './extractors/notification'
import { clineResultDivider } from './extractors/resultDivider'
import { clineExtractRow } from './extractors/row'
import { clineConfiguration } from './pluginConfiguration'
import { clineControls } from './pluginControls'
import { clineRelatedMessages, clineSpanRole } from './spanRole'

const clinePlugin: ProviderPlugin = {
  transcript: {
    classify: classifyClineMessage,
    extractRow: clineExtractRow,
    spanRole: clineSpanRole,
    relatedMessages: clineRelatedMessages,
    extractDivider: clineResultDivider,
    notificationEntry: clineNotificationEntry,
  },
  controls: clineControls,
  session: {
    compactionBoundaryFromMessage: clineCompactionBoundary,
  },
  configuration: clineConfiguration,
}

registerProvider(AgentProvider.CLINE, clinePlugin)
