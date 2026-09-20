import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { pickObject, pickString } from '~/lib/jsonPick'
import { acpExtractControl } from '../acp/extractControl'
import { isCursorCreatePlanPayload } from './askUserQuestion'

/**
 * `Provider.extractControl` for Cursor.
 *
 * Cursor answers two requests of its own and speaks the Agent Client Protocol for
 * the rest, so the fallback DELEGATES rather than repeating the ACP reader. Its
 * control component picked between the two the same way.
 */
export function cursorExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  // A QUESTION never reaches here: `askUserQuestion.isRequest` is the one recognizer,
  // and the control surface answers it before any provider's reader runs.
  if (isCursorCreatePlanPayload(payload)) {
    // Cursor's create-plan request is a plan approval that carries its own name and
    // overview, which no other provider's plan sends. They read as the operation and
    // its reason, which is what a permission body already draws.
    const params = pickObject(payload, 'params')
    const name = pickString(params, 'name')
    const reason = pickString(params, 'overview', undefined)
    return {
      kind: 'permission',
      permission: {
        title: name ? `Create Plan: ${name}` : 'Create Plan',
        ...(reason !== undefined ? { reason } : {}),
        options: [],
      },
    }
  }
  return acpExtractControl(input)
}
