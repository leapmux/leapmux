import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyQoderMessage } from './classification'
import { qoderResultDivider } from './extractors/resultDivider'
import { qoderExtractRow } from './extractors/row'
import { qoderControls } from './pluginControls'
import { qoderRelatedMessages, qoderSpanRole } from './spanRole'

const qoderPlugin: ProviderPlugin = {
  transcript: {
    spanRole: qoderSpanRole,
    relatedMessages: qoderRelatedMessages,
    classify: classifyQoderMessage,
    extractRow: qoderExtractRow,
    extractDivider: qoderResultDivider,
  },
  controls: qoderControls,
  configuration: {
    attachments: {
      text: true,
      image: true,
      pdf: true,
      binary: true,
    },
  },
}

registerProvider(AgentProvider.QODER, qoderPlugin)
