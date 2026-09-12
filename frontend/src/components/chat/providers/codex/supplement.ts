import type { ParsedMessageContent } from '~/lib/messageParser'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Put a tool call's joined output back on the item it belongs to.
 *
 * Codex streams a command's output as a run of `outputDelta` events and fills
 * `aggregatedOutput` only on the COMPLETED item. A turn that ends first leaves the
 * started item, which carries no output, so the worker joins the deltas and stores
 * the join beside the frame rather than inside it.
 *
 * The identity keys are checked first: a supplement that names another item, or
 * another item type, cannot reach this row. The original object stays unchanged, so
 * the Raw JSON view still shows the agent's own bytes.
 */
export function resolveCodexMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const supplement = parsed.supplementalContent
  if (!original || !isObject(supplement))
    return original
  const output = pickString(supplement, 'aggregatedOutput')
  if (!output)
    return original
  const item = pickObject(original, 'item')
  if (!item || pickString(item, 'id') !== pickString(supplement, 'itemId')
    || pickString(item, 'type') !== pickString(supplement, 'itemType')) {
    return original
  }
  return { ...original, item: { ...item, aggregatedOutput: output } }
}
