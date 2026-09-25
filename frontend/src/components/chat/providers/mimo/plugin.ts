import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyMiMoMessage } from './classification'
import { mimoNotificationEntry } from './extractors/notification'
import { mimoResultDivider } from './extractors/resultDivider'
import { mimoExtractRow } from './extractors/row'
import { mimoConfiguration } from './pluginConfiguration'
import { mimoControls } from './pluginControls'
import { mimoRelatedMessages, mimoSpanRole } from './spanRole'

const mimoPlugin: ProviderPlugin = {
  transcript: {
    spanRole: mimoSpanRole,
    relatedMessages: mimoRelatedMessages,
    classify: classifyMiMoMessage,
    extractRow: mimoExtractRow,
    notificationEntry: mimoNotificationEntry,
    extractDivider: mimoResultDivider,
  },
  controls: mimoControls,
  configuration: mimoConfiguration,
}

registerProvider(AgentProvider.MIMO_CODE, mimoPlugin)
