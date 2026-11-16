import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyZCodeMessage } from './classification'
import { zcodeCompactionBoundary, zcodeCompactionStart, zcodeNotificationEntry } from './extractors/notification'
import { zcodeOutputFilePaths } from './extractors/outputFilePaths'
import { zcodeResultDivider } from './extractors/resultDivider'
import { zcodeExtractRow } from './extractors/row'
import { zcodeConfiguration } from './pluginConfiguration'
import { zcodeControls } from './pluginControls'
import { resolveZCodeMessage } from './resolveMessage'
import { zcodeContextUsageFromMessage } from './sessionMetadata'
import { zcodeNeedsRequest, zcodeSpanRole } from './spanRole'

const zcodePlugin: ProviderPlugin = {
  transcript: {
    compactionStart: zcodeCompactionStart,
    outputFilePaths: zcodeOutputFilePaths,
    resolveMessage: resolveZCodeMessage,
    spanRole: zcodeSpanRole,
    needsRequest: zcodeNeedsRequest,
    classify: classifyZCodeMessage,
    extractRow: zcodeExtractRow,
    notificationEntry: zcodeNotificationEntry,
    extractDivider: zcodeResultDivider,
  },
  controls: zcodeControls,
  session: {
    contextUsageFromMessage: zcodeContextUsageFromMessage,
    compactionBoundaryFromMessage: zcodeCompactionBoundary,
  },
  configuration: zcodeConfiguration,
}

registerProvider(AgentProvider.ZCODE, zcodePlugin)
