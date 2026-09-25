import type { ProviderPlugin } from '../capabilities'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { classifyCodewhaleMessage } from './classification'
import { codewhaleNotificationEntry } from './extractors/notification'
import { codewhaleResultDivider } from './extractors/resultDivider'
import { codewhaleExtractRow } from './extractors/row'
import { codewhaleConfiguration } from './pluginConfiguration'
import { codewhaleControls } from './pluginControls'
import { codewhaleRelatedMessages, codewhaleSpanRole } from './spanRole'

const codewhalePlugin: ProviderPlugin = {
  transcript: {
    spanRole: codewhaleSpanRole,
    relatedMessages: codewhaleRelatedMessages,
    classify: classifyCodewhaleMessage,
    extractRow: codewhaleExtractRow,
    notificationEntry: codewhaleNotificationEntry,
    extractDivider: codewhaleResultDivider,
  },
  controls: codewhaleControls,
  configuration: codewhaleConfiguration,
}

registerProvider(AgentProvider.CODEWHALE, codewhalePlugin)
