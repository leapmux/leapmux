import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { registerProvider } from '../registry'
import { classifyLettaMessage } from './classification'
import { lettaResultDivider } from './extractors/resultDivider'
import { lettaExtractRow } from './extractors/row'
import { lettaControls } from './pluginControls'
import { lettaRelatedMessages, lettaSpanRole } from './spanRole'

const lettaPlugin: ProviderPlugin = {
  transcript: {
    classify: classifyLettaMessage,
    extractRow: lettaExtractRow,
    extractDivider: lettaResultDivider,
    spanRole: lettaSpanRole,
    relatedMessages: lettaRelatedMessages,
  },
  controls: lettaControls,
  configuration: {
    // The same policy as the worker's ValidateAttachment.
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
    // Letta's permission mode is `runtime_start.mode`. The status bar draws its
    // chip only for the axis the plugin names, so omitting this hid the mode the
    // session was running.
    triggerModeGroupKey: OPTION_ID_PERMISSION_MODE,
  },
}

registerProvider(AgentProvider.LETTA, lettaPlugin)
