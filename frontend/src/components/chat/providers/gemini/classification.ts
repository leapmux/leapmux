import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput, ProviderTranscriptCapability } from '../capabilities'
import { geminiStoredToolRecord } from './extractors/storedToolCall'
import { geminiNativeMessage, geminiNativePart } from './nativeMessage'

function geminiNativeCategory(input: ClassificationInput): MessageCategory | null {
  if (geminiStoredToolRecord(input.parentObject))
    return { kind: 'tool_use' }
  const message = geminiNativeMessage(input)
  if (!message)
    return null
  const part = geminiNativePart(input, message)
  if (!part)
    return { kind: 'unknown' }
  if (!part.text)
    return { kind: 'hidden' }
  if (part.kind === 'thought')
    return { kind: 'assistant_thinking' }
  return { kind: message.type === 'user' ? 'user_text' : 'assistant_text' }
}

/** Preserve the captured ACP classifier as the fallback for native child rows. */
export function createGeminiClassifier(base: Pick<ProviderTranscriptCapability, 'classify'>): ProviderTranscriptCapability['classify'] {
  return (input, context) => geminiNativeCategory(input) ?? base.classify(input, context)
}
