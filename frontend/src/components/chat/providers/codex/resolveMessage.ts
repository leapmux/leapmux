import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEX_ITEM_FIELD, CODEX_SUPPLEMENT } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'

/**
 * The joined output the worker stored for a tool call that reported no completed item.
 *
 * The three keys are contract constants the worker writes
 * (contracts/codex-protocol.json), so a rename cannot leave one language reading a key
 * the other stopped writing -- which used to draw a retained command row with no
 * output at all and nothing to say a supplement had been dropped.
 */
interface CodexToolSupplement {
  itemId: string
  itemType: string
  aggregatedOutput: string
}

/**
 * The stored envelope, or undefined when it states no joined output.
 *
 * Every field must decode the way the WORKER decodes it, which
 * {@link workerString} states. The item id must then name something, and the output
 * must carry something. Without the type test a frame whose `itemType` arrived as a
 * number took the join here and not in the worker, so the browser and the worker's own
 * extractors read two different rows.
 * testdata/codex_message_content_conformance.json replays both sides.
 */
function codexToolSupplement(supplemental: unknown): CodexToolSupplement | undefined {
  if (!isObject(supplemental))
    return undefined
  // The worker decodes the WHOLE envelope into one struct, so a missing key simply
  // stays empty and one bad field drops every field.
  const itemId = workerString(supplemental[CODEX_SUPPLEMENT.ItemID], '')
  const itemType = workerString(supplemental[CODEX_SUPPLEMENT.ItemType], '')
  const aggregatedOutput = workerString(supplemental[CODEX_SUPPLEMENT.AggregatedOutput], '')
  if (itemId === null || itemType === null || aggregatedOutput === null)
    return undefined
  if (!itemId || !aggregatedOutput)
    return undefined
  return { itemId, itemType, aggregatedOutput }
}

/**
 * One field as the WORKER decodes it, or null for a value its decode refuses.
 *
 * Go's `json.Unmarshal` into a `string` leaves the field EMPTY for a JSON `null` and
 * fails for every other type that is not a string. `absent` states what a missing key
 * does, which differs by side and is not a detail: the worker decodes the whole
 * supplement into one struct, where a missing key stays empty, and it decodes each
 * FRAME field on its own, where a missing key is nil bytes that `json.Unmarshal`
 * refuses.
 */
function workerString(value: unknown, absent: '' | null): string | null {
  if (value === undefined)
    return absent
  if (value === null)
    return ''
  return typeof value === 'string' ? value : null
}

/**
 * Put a tool call's joined output back on the item it belongs to.
 *
 * Codex streams a command's output as a run of `outputDelta` events and fills
 * `aggregatedOutput` only on the COMPLETED item. A turn that ends first leaves the
 * started item, which carries no output, so the worker joins the deltas and stores the
 * join beside the frame rather than inside it.
 *
 * The identity keys are checked first: a supplement that identifies another item, or
 * another item type, cannot reach this row. The original object stays unchanged, so
 * the Raw JSON view still shows the agent's own bytes.
 */
export function resolveCodexMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const supplement = codexToolSupplement(parsed.supplementalContent)
  if (!original || !supplement)
    return original
  // Both identity fields decode the way the worker decodes them: a JSON `null` reads
  // as empty, an absent field refuses the join, and so does any other type. A frame
  // whose `type` arrived as `null` used to take the join in the worker and not here,
  // so the worker's own extractors and the row on screen carried different output.
  const item = pickObject(original, CODEX_ITEM_FIELD.Envelope)
  if (!item || workerString(item[CODEX_ITEM_FIELD.ID], null) !== supplement.itemId
    || workerString(item[CODEX_ITEM_FIELD.Type], null) !== supplement.itemType) {
    return original
  }
  return {
    ...original,
    [CODEX_ITEM_FIELD.Envelope]: { ...item, [CODEX_ITEM_FIELD.AggregatedOutput]: supplement.aggregatedOutput },
  }
}
