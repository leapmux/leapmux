import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyCommandCodeMessage } from './classification'
import { commandCodeCompactionBoundary, commandCodeNotificationEntry } from './extractors/notification'
import { commandCodeOutputFilePaths } from './extractors/outputFilePaths'
import { commandCodeResultDivider } from './extractors/resultDivider'
import { commandCodeExtractRow } from './extractors/row'
import { commandCodePermissionPresets } from './permissionPresets'
import { commandCodeConfiguration } from './pluginConfiguration'
import { commandCodeRelatedMessages, commandCodeSpanRole } from './spanRole'

const commandCodePlugin: ProviderPlugin = {
  transcript: {
    outputFilePaths: commandCodeOutputFilePaths,
    classify: classifyCommandCodeMessage,
    spanRole: commandCodeSpanRole,
    relatedMessages: commandCodeRelatedMessages,
    extractRow: commandCodeExtractRow,
    extractDivider: commandCodeResultDivider,
    notificationEntry: commandCodeNotificationEntry,
  },
  controls: { permissionPresets: commandCodePermissionPresets },
  session: { compactionBoundaryFromMessage: commandCodeCompactionBoundary },
  configuration: commandCodeConfiguration,
}

registerProvider(AgentProvider.COMMAND_CODE, commandCodePlugin)
