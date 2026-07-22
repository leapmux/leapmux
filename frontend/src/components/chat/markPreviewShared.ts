import type { MessageCategory } from './messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { pickString } from '~/lib/jsonPick'
import { truncatePreview } from '~/lib/textTruncate'

// ---------------------------------------------------------------------------
// Scroll-rail mark preview -- shared, provider-neutral extraction
//
// A LEAF module w.r.t. the chat plugin machinery (it imports only lib utils:
// jsonPick, and textTruncate -- which pulls the shared markdown parser for
// markdown-safe truncation) so every provider plugin can import
// `defaultMarkPreview` as the fallback for its `Provider.previewText` WITHOUT pulling in
// the plugin registry / classifier (chatMarkPreview.ts, which orchestrates the plugins) --
// that would be a module-init cycle. Provider-specific extraction lives in each plugin's
// `previewText`; this covers only the Leapmux-neutral marked shapes.
// ---------------------------------------------------------------------------

// The control-response display labels moved to persistedControlResponse.ts (the leaf that owns
// control-response display); this "preview" module no longer references them, and persisted
// control-response rows resolve their preview through the plugin's controlResponseDisplay in
// chatMarkPreview.ts, never here.

/**
 * Provider-NEUTRAL preview extractor and shared fallback for every provider's
 * {@link Provider.previewText}. Handles only the Leapmux-neutral user-send shape: the app persists
 * user sends (and forwarded plan-prompt feedback) as `{content:"..."}` regardless of provider.
 * Provider-specific shapes (e.g. Claude's Anthropic tool_result blocks and its `{message:{content}}`
 * transcript envelope) are handled by that provider's `previewText` BEFORE it falls back here.
 * Persisted control-response rows are NOT handled here -- they classify as `control_response` and
 * the rail resolves their preview through the plugin's `controlResponseDisplay` (chatMarkPreview.ts).
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

  return null
}
