import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyMuseMessage, museSpanRole } from './classification'
import { museControl } from './control'
import { museControlResponseSummary } from './controlResponse'
import { museCompactionBoundary, museNotificationEntry } from './extractors/notification'
import { museOutputFilePaths } from './extractors/outputFilePaths'
import { museResultDivider } from './extractors/resultDivider'
import { museExtractRow } from './extractors/row'
import { musePermissionPresets } from './permissionPresets'
import { museConfiguration } from './pluginConfiguration'

const musePlugin: ProviderPlugin = {
  transcript: {
    classify: classifyMuseMessage,
    spanRole: museSpanRole,
    extractRow: museExtractRow,
    extractDivider: museResultDivider,
    outputFilePaths: museOutputFilePaths,
    notificationEntry: museNotificationEntry,
  },
  controls: { ...museControl, permissionPresets: musePermissionPresets, controlResponseDisplay: museControlResponseSummary },
  session: { compactionBoundaryFromMessage: museCompactionBoundary },
  configuration: museConfiguration,
}

registerProvider(AgentProvider.MUSE_CODE, musePlugin)
