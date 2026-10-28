import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationContext, ClassificationInput } from '../capabilities'
import { fastAgentACPPlugin } from './acpPlugin'
import { fastAgentArchiveSource, hasFastAgentArchiveSource } from './archiveSource'

export function classifyFastAgentMessage(input: ClassificationInput, context?: ClassificationContext): MessageCategory {
  const source = fastAgentArchiveSource(input)
  if (!source)
    return hasFastAgentArchiveSource(input) ? { kind: 'unknown' } : fastAgentACPPlugin.transcript.classify(input, context)
  switch (source.kind) {
    case 'content': return { kind: 'assistant_text' }
    case 'reasoning': return { kind: 'assistant_thinking' }
    case 'error': return { kind: 'assistant_text' }
    case 'request':
    case 'result': return { kind: 'tool_use' }
    case 'metadata': return { kind: 'hidden' }
  }
}
