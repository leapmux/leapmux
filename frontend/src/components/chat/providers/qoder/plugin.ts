import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyQoderMessage } from './classification'
import { qoderCompactionBoundary, qoderNotificationEntry } from './extractors/notification'
import { qoderResultDivider } from './extractors/resultDivider'
import { qoderExtractRow } from './extractors/row'
import { qoderControls } from './pluginControls'
import { qoderRelatedMessages, qoderSpanRole } from './spanRole'

const qoderPlugin: ProviderPlugin = {
  transcript: {
    spanRole: qoderSpanRole,
    relatedMessages: qoderRelatedMessages,
    classify: classifyQoderMessage,
    notificationEntry: qoderNotificationEntry,
    extractRow: qoderExtractRow,
    extractDivider: qoderResultDivider,
  },
  controls: qoderControls,
  session: {
    compactionBoundaryFromMessage: qoderCompactionBoundary,
  },
  configuration: {
    triggerModeGroupKey: OPTION_ID_PERMISSION_MODE,
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
  },
}

registerProvider(AgentProvider.QODER, qoderPlugin)
