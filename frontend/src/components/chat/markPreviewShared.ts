import type { MessageCategory } from './messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { isObject, pickString } from '~/lib/jsonPick'
import { truncatePreview } from '~/lib/textTruncate'

// ---------------------------------------------------------------------------
// Scroll-rail mark preview -- shared, provider-neutral extraction
//
// A LEAF module (only pure json/text utils) so every provider plugin can import
// `defaultMarkPreview` as the fallback for its `Provider.previewText` WITHOUT pulling in
// the plugin registry / classifier (chatMarkPreview.ts, which orchestrates the plugins) --
// that would be a module-init cycle. Provider-specific extraction lives in each plugin's
// `previewText`; this covers only the Leapmux-neutral marked shapes.
// ---------------------------------------------------------------------------

// The three user-facing labels for a persisted {controlResponse} answer row. Shared by the row's
// renderer (controlResponseRenderer in notificationRenderers) AND its scroll-rail dot preview
// (defaultMarkPreview below) so the dot reads IDENTICALLY to the row it jumps to -- the wording
// lives here once instead of being mirrored by hand across the two surfaces (the drift a comment
// alone can't prevent). Kept in this leaf so the preview extractor and the renderer can both import
// them without a module-init cycle.
/** An approved control request (ExitPlanMode approval, an "allow" permission decision). */
export const CONTROL_RESPONSE_APPROVED_LABEL = 'Approved'
/** A declined control request with no typed reason. */
export const CONTROL_RESPONSE_REJECTED_LABEL = 'Rejected'
/** Lead-in shown above the user's typed rejection reason (their feedback follows as markdown). */
export const CONTROL_RESPONSE_FEEDBACK_LEAD = 'Sent feedback:'

/**
 * Provider-NEUTRAL preview extractor and shared fallback for every provider's
 * {@link Provider.previewText}. Handles only the Leapmux-neutral marked shapes: the
 * app persists user sends as `{content:"..."}` and control-request responses as
 * `{controlResponse:{action,comment}}` regardless of provider. Provider-specific shapes
 * (e.g. Claude's Anthropic tool_result blocks and its `{message:{content}}` transcript
 * envelope) are handled by that provider's `previewText` BEFORE it falls back here.
 */
export function defaultMarkPreview(_category: MessageCategory, parsed: ParsedMessageContent): string | null {
  const obj = parsed.parentObject
  if (!obj)
    return null

  // User-typed input: the Leapmux-neutral `{content:"..."}` shape (every provider).
  // Assistant messages store `content` as a block array, so a string test here
  // never mis-picks them.
  const content = pickString(obj, 'content', '')
  if (content)
    return truncatePreview(content)

  // Control-request response display row -- mirror controlResponseRenderer (notificationRenderers),
  // the row the dot actually jumps to, so the preview reads the same as that row: an approved row
  // shows "Approved"; a rejection carrying the user's typed reason shows "Sent feedback:" above the
  // reason (NOT the inline ControlResponseTag's "Rejected: <reason>", which is a different surface);
  // a bare rejection shows "Rejected".
  const cr = isObject(obj.controlResponse) ? obj.controlResponse : undefined
  if (cr) {
    const action = pickString(cr, 'action', '')
    const comment = pickString(cr, 'comment', '')
    if (action === 'approved')
      return CONTROL_RESPONSE_APPROVED_LABEL
    return comment ? truncatePreview(`${CONTROL_RESPONSE_FEEDBACK_LEAD}\n${comment}`) : CONTROL_RESPONSE_REJECTED_LABEL
  }

  return null
}
