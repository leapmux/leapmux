import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyKimiMessage } from './classification'
import { kimiCompactionBoundary, kimiNotificationEntry } from './extractors/notification'
import { kimiOutputFilePaths } from './extractors/outputFilePaths'
import { kimiResultDivider } from './extractors/resultDivider'
import { kimiExtractRow } from './extractors/row'
import { kimiConfiguration } from './pluginConfiguration'
import { kimiControls } from './pluginControls'
import { resolveKimiMessage } from './resolveMessage'
import { kimiNeedsRequest, kimiSpanRole } from './spanRole'

const kimiPlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveKimiMessage,
    outputFilePaths: kimiOutputFilePaths,
    classify: classifyKimiMessage,
    extractRow: kimiExtractRow,
    spanRole: kimiSpanRole,
    needsRequest: kimiNeedsRequest,
    extractDivider: kimiResultDivider,
    notificationEntry: kimiNotificationEntry,
  },
  controls: kimiControls,
  session: {
    compactionBoundaryFromMessage: kimiCompactionBoundary,
  },
  configuration: kimiConfiguration,
}

registerProvider(AgentProvider.KIMI_CODE, kimiPlugin)
