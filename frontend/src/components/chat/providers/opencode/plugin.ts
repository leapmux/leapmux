import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyOpenCodeMessage } from './classification'
import { openCodeCompactionStart, openCodeNotificationEntry } from './extractors/notification'
import { openCodeOutputFilePaths } from './extractors/outputFilePaths'
import { openCodeResultDivider } from './extractors/resultDivider'
import { openCodeExtractRow } from './extractors/row'
import { openCodeSpanRole } from './extractors/toolCommon'
import { openCodeConfiguration } from './pluginConfiguration'
import { openCodeControls } from './pluginControls'
import { resolveOpenCodeMessage } from './resolveMessage'

const openCodePlugin: ProviderPlugin = {
  transcript: {
    resolveMessage: resolveOpenCodeMessage,
    classify: classifyOpenCodeMessage,
    spanRole: openCodeSpanRole,
    extractRow: openCodeExtractRow,
    outputFilePaths: openCodeOutputFilePaths,
    extractDivider: openCodeResultDivider,
    notificationEntry: openCodeNotificationEntry,
    compactionStart: openCodeCompactionStart,
  },
  controls: openCodeControls,
  configuration: openCodeConfiguration,
}

registerProvider(AgentProvider.OPENCODE, openCodePlugin)
