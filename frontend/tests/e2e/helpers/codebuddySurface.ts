import type { MockModelProtocol } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { holdsAncestorInstructions, holdsOnlySentinelDocuments } from './ancestorInstructions'

/** The line that opens the system prompt of each request of CodeBuddy's Auto-mode classifier. */
export const CODEBUDDY_CLASSIFIER_SYSTEM_OPENING = 'You are a security monitor for autonomous AI coding agents.'

/** The sentence that opens the classifier's context message, which quotes the memory files. */
const CODEBUDDY_CLASSIFIER_MEMORY_OPENING = 'The following is the user\'s CODEBUDDY.md configuration.'

/** The classifier's context message: its fixed preamble, then the memory files in one tagged block that ends it. */
const CODEBUDDY_CLASSIFIER_MEMORY_MESSAGE = new RegExp(`^${escapeRegExp(CODEBUDDY_CLASSIFIER_MEMORY_OPENING)}([\\s\\S]*?)\\n<user_codebuddy_md>\\n([\\s\\S]*)\\n</user_codebuddy_md>$`)

/**
 * The request body without the memory message of CodeBuddy's Auto-mode classifier, when that message quotes only the
 * sentinel documents of the run root (`./ancestorInstructions.ts`). Every other body stays as it is.
 *
 * In Auto mode, CodeBuddy Code 2.174.0 asks a classifier on the session model before each call that would prompt. The
 * classifier quotes the user memory and the project memory in a context message of its own
 * (`AutoModeProjectInstructionsResolver`). It trims each memory file and joins the files with a blank line. The project
 * memory comes from each directory up to the root of the file system (`MemoryLoader.loadProjectMemories`), and no
 * setting or git repository stops that walk. CODEBUDDY_DISABLE_SYSTEM_REMINDER_MD turns off only the memory reminder of
 * the conversation (`./codebuddyEnvironment.ts`). The classifier therefore always quotes the sentinel documents of the
 * run root.
 *
 * The body check skips that one message and checks the rest of the request. The message keeps the refusal when its
 * memory holds any text other than the sentinel documents, or when its preamble holds the sentinel.
 */
export function withoutCodebuddyClassifierSentinelMemory(protocol: MockModelProtocol, body: unknown): unknown {
  if (protocol !== 'openai-chat-completions' || !isObject(body) || !Array.isArray(body.messages))
    return body
  const system: unknown = body.messages[0]
  if (!isObject(system) || system.role !== 'system' || typeof system.content !== 'string' || !system.content.startsWith(CODEBUDDY_CLASSIFIER_SYSTEM_OPENING))
    return body
  const messages = body.messages.filter(message => !quotesOnlySentinelMemory(message))
  return messages.length === body.messages.length ? body : { ...body, messages }
}

/** Whether `message` is the classifier's memory message, and its memory holds only sentinel documents. */
function quotesOnlySentinelMemory(message: unknown): boolean {
  if (!isObject(message) || message.role !== 'user' || !Array.isArray(message.content) || message.content.length !== 1)
    return false
  const part: unknown = message.content[0]
  if (!isObject(part) || part.type !== 'text' || typeof part.text !== 'string')
    return false
  const match = CODEBUDDY_CLASSIFIER_MEMORY_MESSAGE.exec(part.text)
  if (!match)
    return false
  const [, preamble = '', memory = ''] = match
  return !holdsAncestorInstructions(preamble) && holdsOnlySentinelDocuments(memory)
}
