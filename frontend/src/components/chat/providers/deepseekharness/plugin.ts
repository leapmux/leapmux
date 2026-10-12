import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyDeepseekHarnessMessage } from './classification'
import { deepseekHarnessCompactionBoundary, deepseekHarnessNotificationEntry } from './extractors/notification'
import { deepseekHarnessOutputFilePaths } from './extractors/outputFilePaths'
import { deepseekHarnessResultDivider } from './extractors/resultDivider'
import { deepseekHarnessExtractRow } from './extractors/row'
import { deepseekHarnessConfiguration } from './pluginConfiguration'
import { deepseekHarnessControls } from './pluginControls'
import { resolveDeepseekHarnessMessage } from './resolveMessage'
import { deepseekHarnessContextUsage } from './sessionMetadata'
import { deepseekHarnessRelatedMessages, deepseekHarnessSpanRole } from './spanRole'

const deepseekHarnessPlugin: ProviderPlugin = {
  transcript: {
    outputFilePaths: deepseekHarnessOutputFilePaths,
    resolveMessage: resolveDeepseekHarnessMessage,
    classify: classifyDeepseekHarnessMessage,
    spanRole: deepseekHarnessSpanRole,
    relatedMessages: deepseekHarnessRelatedMessages,
    extractRow: deepseekHarnessExtractRow,
    extractDivider: deepseekHarnessResultDivider,
    notificationEntry: deepseekHarnessNotificationEntry,
  },
  controls: deepseekHarnessControls,
  configuration: deepseekHarnessConfiguration,
  session: {
    contextUsageFromMessage: deepseekHarnessContextUsage,
    compactionBoundaryFromMessage: deepseekHarnessCompactionBoundary,
  },
}

registerProvider(AgentProvider.DEEPSEEK_HARNESS, deepseekHarnessPlugin)
