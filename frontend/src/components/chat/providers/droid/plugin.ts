import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyDroidMessage } from './classification'
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
  configuration: {
    // The same policy as the worker's ValidateAttachment.
    attachments: {
      text: true,
      image: true,
      pdf: false,
      binary: false,
    },
  },
}

registerProvider(AgentProvider.DROID, droidPlugin)
