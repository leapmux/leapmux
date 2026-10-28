import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerProvider } from '../registry'
import { fastAgentACPPlugin } from './acpPlugin'
import { classifyFastAgentMessage } from './classification'
import { fastAgentOutputFilePaths } from './extractors/outputFilePaths'
import { fastAgentExtractRow } from './extractors/row'
import { resolveFastAgentMessage } from './resolveMessage'
import { fastAgentRelatedMessages, fastAgentSpanRole } from './spanRole'

registerProvider(AgentProvider.FAST_AGENT, {
  ...fastAgentACPPlugin,
  transcript: {
    ...fastAgentACPPlugin.transcript,
    resolveMessage: resolveFastAgentMessage,
    classify: classifyFastAgentMessage,
    spanRole: fastAgentSpanRole,
    relatedMessages: fastAgentRelatedMessages,
    extractRow: fastAgentExtractRow,
    outputFilePaths: fastAgentOutputFilePaths,
  },
})
