import type { ProviderPlugin } from '../capabilities'
import { AMP_OPTION } from '~/generated/contracts/amp-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyAmpMessage } from './classification'
import { ampResultDivider } from './extractors/resultDivider'
import { ampExtractRow } from './extractors/row'
import { ampControls } from './pluginControls'
import { ampRelatedMessages, ampSpanRole } from './spanRole'

const ampPlugin: ProviderPlugin = {
  transcript: {
    spanRole: ampSpanRole,
    relatedMessages: ampRelatedMessages,
    classify: classifyAmpMessage,
    extractRow: ampExtractRow,
    extractDivider: ampResultDivider,
  },
  controls: ampControls,
  configuration: {
    // The same policy as the worker's ValidateAttachment: Amp's stdin line carries text
    // and image blocks, and it has no block for a PDF or another binary file.
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
    // Amp's one mode axis is its agent mode, which chooses the model and the effort.
    triggerModeGroupKey: AMP_OPTION.AgentMode,
  },
}

registerProvider(AgentProvider.AMP, ampPlugin)
