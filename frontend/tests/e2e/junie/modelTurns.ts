import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModelTurn } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'

/**
 * The native context blocks Junie states as whole user rows of its own:
 * the capability listing it sends before the issue, and the project listing it
 * sends after it. Both are the model's input, not the conversation, so the
 * reader classifies them as context and returns no user turn for either.
 */
const NATIVE_CONTEXT_ROW = /^## (?:CAPABILITIES CONTEXT|PROJECT STRUCTURE)\n/

/**
 * Read the turns of a native Junie request in request order, with the current prompt last.
 *
 * Junie does not restate its transcript as alternating turns. Its history processor
 * compresses the previous exchange into ONE user row: the issue it worked on inside
 * `<previous_issue>` and its own answer inside `<previous_issue_solution>`. Those two
 * blocks are the prior user and assistant turns in request order, and every further
 * user row keeps its place as a user turn unless it is one of Junie's own context
 * blocks, which the reader drops.
 */
export function junieModelTurns(request: MockModelRequestRecord): NativeModelTurn[] {
  if (request.protocol !== 'openai-chat-completions')
    throw new Error('The Junie turn reader requires its native chat-completions request.')
  const body = request.body
  if (!isObject(body) || !Array.isArray(body.messages))
    throw new Error('The native Junie request contains no message array.')
  return body.messages.flatMap((message: unknown): NativeModelTurn[] => {
    if (!isObject(message))
      return []
    if (message.role !== 'user' || typeof message.content !== 'string')
      return []
    if (NATIVE_CONTEXT_ROW.test(message.content))
      return []
    const turns: NativeModelTurn[] = []
    // The anchors keep the pattern on the real blocks: the row's prose names each
    // tag inside backticks first, and only the block itself starts a line.
    const issue = /^<previous_issue>\n([\s\S]*?)\n<\/previous_issue>$/m.exec(message.content)
    if (issue)
      turns.push({ role: 'user', text: (issue[1] ?? '').trim() })
    const solution = /^<previous_issue_solution>\n?([\s\S]*?)<\/previous_issue_solution>$/m.exec(message.content)
    if (solution)
      turns.push({ role: 'assistant', text: (solution[1] ?? '').trim() })
    if (turns.length === 0)
      turns.push({ role: 'user', text: message.content })
    return turns
  })
}
