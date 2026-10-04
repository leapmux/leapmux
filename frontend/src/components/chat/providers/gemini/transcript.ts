import type { ChatRow } from '../../model/row'
import type { RowExtractionInput } from '../../rowExtractionTypes'
import type { ProviderTranscriptCapability } from '../capabilities'
import { toolCallRow } from '../../model/row'
import { createGeminiClassifier } from './classification'
import { geminiStoredToolCall, geminiStoredToolRecord } from './extractors/storedToolCall'
import { geminiNativeMessage, geminiNativePart } from './nativeMessage'

function geminiNativeRow(input: RowExtractionInput): ChatRow | null {
  const record = geminiStoredToolRecord(input.resolved.parentObject)
  if (record)
    return toolCallRow(geminiStoredToolCall(record, input.completion), 'result', input.span.visibleRows)
  const message = geminiNativeMessage(input.resolved)
  if (!message)
    return null
  const part = geminiNativePart(input.resolved, message)
  if (!part)
    return null
  if (!part.text)
    return { kind: 'hidden' }
  if (part.kind === 'thought')
    return { kind: 'assistant-thinking', text: part.text }
  return message.type === 'user' ? { kind: 'user', text: part.text, attachments: [] } : { kind: 'assistant-text', text: part.text }
}

/** Compose captured ACP hooks with Gemini's exact native child-record readers. */
export function composeGeminiTranscript(base: ProviderTranscriptCapability): ProviderTranscriptCapability {
  return {
    ...base,
    resolveMessage: parsed => geminiNativeMessage(parsed) || geminiStoredToolRecord(parsed.parentObject) ? parsed.parentObject : base.resolveMessage?.(parsed),
    classify: createGeminiClassifier(base),
    spanRole: parsed => geminiStoredToolRecord(parsed.parentObject) ? 'result' : geminiNativeMessage(parsed) ? 'other' : base.spanRole(parsed),
    relatedMessages: parsed => geminiNativeMessage(parsed) || geminiStoredToolRecord(parsed.parentObject) ? [] : base.relatedMessages?.(parsed) ?? [],
    extractRow: input => geminiNativeMessage(input.resolved) || geminiStoredToolRecord(input.resolved.parentObject) ? geminiNativeRow(input) : base.extractRow(input),
  }
}
