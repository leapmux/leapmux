import type { ParsedMessageContent } from '~/lib/messageParser'
import { GEMINI_MESSAGE_PART, GEMINI_SUPPLEMENT } from '~/generated/contracts/gemini-protocol'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'

export function geminiNativeMessage(parsed: ParsedMessageContent): Record<string, unknown> | null {
  const message = parsed.parentObject
  if (!message || pickString(message, 'id') === '' || !['user', 'gemini', 'info', 'warning', 'error'].includes(pickString(message, 'type')))
    return null
  return typeof message.content === 'string' || Array.isArray(message.content) ? message : null
}

function geminiNativeText(value: unknown): string | null {
  if (typeof value === 'string')
    return value
  if (!Array.isArray(value))
    return null
  return value.filter(isObject).filter(part => typeof part.text === 'string' && part.thought !== true).map(part => pickString(part, 'text')).join('')
}

export function geminiNativePart(parsed: ParsedMessageContent, message: Record<string, unknown>): { kind: 'content' | 'thought', text: string } | null {
  const supplement = isObject(parsed.supplementalContent) ? parsed.supplementalContent : undefined
  const part = pickString(supplement, GEMINI_SUPPLEMENT.MessagePart)
  if (part === '' || part === GEMINI_MESSAGE_PART.Content) {
    const text = geminiNativeText(message.content)
    return text === null ? null : { kind: 'content', text }
  }
  const index = pickNumber(supplement, GEMINI_SUPPLEMENT.MessagePartIndex, -1)
  if (part !== GEMINI_MESSAGE_PART.Thought || !Number.isSafeInteger(index) || index < 0 || !Array.isArray(message.thoughts))
    return null
  const thought: unknown = message.thoughts[index]
  if (!isObject(thought) || typeof thought.description !== 'string')
    return null
  const subject = pickString(thought, 'subject')
  const text = subject ? `**${subject}**\n${thought.description}` : thought.description
  return { kind: 'thought', text }
}
