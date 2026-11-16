import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyDeepseekHarnessMessage } from './classification'
import { deepseekHarnessCompactionBoundary, deepseekHarnessCompactionStart, deepseekHarnessNotificationEntry } from './extractors/notification'
import { deepseekHarnessOutputFilePaths } from './extractors/outputFilePaths'
import { deepseekHarnessResultDivider } from './extractors/resultDivider'
import { deepseekHarnessExtractRow } from './extractors/row'
import { deepseekHarnessConfiguration } from './pluginConfiguration'
import { deepseekHarnessControls } from './pluginControls'
import { resolveDeepseekHarnessMessage } from './resolveMessage'
import { deepseekHarnessContextUsage } from './sessionMetadata'
import { deepseekHarnessNeedsRequest, deepseekHarnessSpanRole } from './spanRole'

const deepseekHarnessPlugin: ProviderPlugin = {
  transcript: {
    outputFilePaths: deepseekHarnessOutputFilePaths,
    resolveMessage: resolveDeepseekHarnessMessage,
    classify: classifyDeepseekHarnessMessage,
    spanRole: deepseekHarnessSpanRole,
    needsRequest: deepseekHarnessNeedsRequest,
    extractRow: deepseekHarnessExtractRow,
    extractDivider: deepseekHarnessResultDivider,
    notificationEntry: deepseekHarnessNotificationEntry,
    compactionStart: deepseekHarnessCompactionStart,
  },
  controls: deepseekHarnessControls,
  configuration: deepseekHarnessConfiguration,
  session: {
    contextUsageFromMessage: deepseekHarnessContextUsage,
    compactionBoundaryFromMessage: deepseekHarnessCompactionBoundary,
  },
}

registerProvider(AgentProvider.DEEPSEEK_HARNESS, deepseekHarnessPlugin)
