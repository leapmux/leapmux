import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { toolCallRow } from '../../../model/row'
import { fastAgentACPPlugin } from '../acpPlugin'
import { fastAgentArchiveSource, hasFastAgentArchiveSource } from '../archiveSource'
import { fastAgentArchiveToolCall } from './archiveToolCall'

export function fastAgentExtractRow(input: RowExtractionInput): ChatRow | null {
  const source = fastAgentArchiveSource(input.resolved)
  if (!source)
    return hasFastAgentArchiveSource(input.resolved) ? null : fastAgentACPPlugin.transcript.extractRow(input)
  switch (source.kind) {
    case 'content': return { kind: 'assistant-text', text: source.text }
    case 'reasoning': return { kind: 'assistant-thinking', text: source.text }
    case 'error': return { kind: 'assistant-text', text: source.text }
    case 'metadata': return { kind: 'hidden' }
    case 'request':
    case 'result': {
      const call = fastAgentArchiveToolCall(input)
      return call ? toolCallRow(call, source.kind === 'request' ? 'request' : 'result', input.span.visibleRows) : null
    }
  }
}
