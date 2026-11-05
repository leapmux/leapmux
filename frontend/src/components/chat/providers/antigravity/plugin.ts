import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyAntigravityMessage } from './classification'
import { antigravityNotificationEntry } from './extractors/notification'
import { antigravityOutputFilePaths } from './extractors/outputFilePaths'
import { antigravityResultDivider } from './extractors/resultDivider'
import { antigravityExtractRow } from './extractors/row'
import { antigravityConfiguration } from './pluginConfiguration'
import { antigravityControls } from './pluginControls'
import { antigravityRelatedMessages, antigravitySpanRole } from './spanRole'

const antigravityPlugin: ProviderPlugin = {
  transcript: {
    outputFilePaths: antigravityOutputFilePaths,
    spanRole: antigravitySpanRole,
    relatedMessages: antigravityRelatedMessages,
    classify: classifyAntigravityMessage,
    extractRow: antigravityExtractRow,
    notificationEntry: antigravityNotificationEntry,
    extractDivider: antigravityResultDivider,
  },
  controls: antigravityControls,
  configuration: antigravityConfiguration,
}

registerProvider(AgentProvider.ANTIGRAVITY_CLI, antigravityPlugin)
