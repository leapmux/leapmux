import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyCodebuddyMessage } from './classification'
import { codebuddyResultDivider } from './extractors/resultDivider'
import { codebuddyExtractRow } from './extractors/row'
import { codebuddyControls } from './pluginControls'
import { codebuddyRelatedMessages, codebuddySpanRole } from './spanRole'

const codebuddyPlugin: ProviderPlugin = {
  transcript: {
    spanRole: codebuddySpanRole,
    relatedMessages: codebuddyRelatedMessages,
    classify: classifyCodebuddyMessage,
    extractRow: codebuddyExtractRow,
    extractDivider: codebuddyResultDivider,
  },
  controls: codebuddyControls,
  configuration: {
    attachments: {
      text: true,
      image: true,
      pdf: true,
      binary: true,
    },
  },
}

registerProvider(AgentProvider.CODEBUDDY, codebuddyPlugin)
