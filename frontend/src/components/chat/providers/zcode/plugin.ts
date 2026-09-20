import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyZCodeMessage } from './classification'
import { zcodeNotificationEntry } from './extractors/notification'
import { zcodeResultDivider } from './extractors/resultDivider'
import { zcodeExtractRow } from './extractors/row'
import { zcodeConfiguration } from './pluginConfiguration'
import { zcodeControls } from './pluginControls'
import { resolveZCodeMessage } from './resolveMessage'
import { zcodeContextUsageFromMessage } from './sessionMetadata'
import { zcodeRelatedMessages, zcodeSpanRole } from './spanRole'

const zcodePlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveZCodeMessage,
    spanRole: zcodeSpanRole,
    relatedMessages: zcodeRelatedMessages,
    classify: classifyZCodeMessage,
    extractRow: zcodeExtractRow,
    notificationEntry: zcodeNotificationEntry,
    extractDivider: zcodeResultDivider,
  },
  controls: zcodeControls,
  session: {
    contextUsageFromMessage: zcodeContextUsageFromMessage,
  },
  configuration: zcodeConfiguration,
}

registerProvider(AgentProvider.ZCODE, zcodePlugin)
