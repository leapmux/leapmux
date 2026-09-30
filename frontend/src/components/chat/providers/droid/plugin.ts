import type { ProviderPlugin } from '../capabilities'
import { DROID_MODE } from '~/generated/contracts/droid-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildPlanMode } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyDroidMessage } from './classification'
import { droidCompactionBoundary } from './extractors/notification'
import { droidResultDivider } from './extractors/resultDivider'
import { droidExtractRow } from './extractors/row'
import { droidControls } from './pluginControls'
import { droidRelatedMessages, droidSpanRole } from './spanRole'

const droidPlugin: ProviderPlugin = {
  transcript: {
    classify: classifyDroidMessage,
    extractRow: droidExtractRow,
    extractDivider: droidResultDivider,
    spanRole: droidSpanRole,
    relatedMessages: droidRelatedMessages,
  },
  controls: droidControls,
  session: {
    compactionBoundaryFromMessage: droidCompactionBoundary,
  },
  configuration: {
    // The same policy as the worker's ValidateAttachment.
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
    planMode: buildPlanMode('permissionMode', DROID_MODE.Spec, DROID_MODE.Default),
    triggerModeGroupKey: 'permissionMode',
  },
}

registerProvider(AgentProvider.DROID, droidPlugin)
