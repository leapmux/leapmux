import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModelTurn } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'

/**
 * Read the turns of a native Junie request in request order, with the current prompt last.
 *
 * Junie does not restate its transcript as alternating turns. Its history processor
 * compresses the previous exchange into ONE user row: the issue it worked on inside
 * `<previous_issue>` and its own answer inside `<previous_issue_solution>`. Those two
 * blocks are the prior user and assistant turns in request order, and every further
 * user row, such as the new issue description, keeps its place as a user turn.
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
