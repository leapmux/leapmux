/**
 * The custom properties the chat ROOT publishes, and the one size that decides how
 * wide a touch target must be.
 *
 * Four stylesheets read these names -- `~/components/chat/ChatView.css.ts` declares
 * them, `~/components/chat/messageStyles.css.ts` cancels the gutter to bleed a row
 * to the panel edge, `~/components/chat/ChatScrollRail.css.ts` sizes its column from
 * the right gutter, and `./widgets/SpanLines.geometry.ts` adds the gutter to the
 * rails' reserved width. Every reader also carries a `, 0px` fallback, so a typo in
 * one spelling does not fail the build: the bleed collapses to zero and the band
 * stops at the gutter with no error at all. One exported name for each property
 * makes that mistake a type error instead.
 *
 * This module imports nothing on purpose. `ChatView.css.ts` already imports
 * `messageStyles.css.ts`, so the names cannot live in either one without a cycle
 * through the stylesheet graph, and a leaf is reachable from all four.
 */

/** Distance from the chat list's left edge to the first content column. */
export const CHAT_PAD_LEFT_VAR = '--chat-pad-left'

/** Distance from the chat list's right edge to the content column. The scroll rail fills it. */
export const CHAT_PAD_RIGHT_VAR = '--chat-pad-right'

/**
 * Width of the scroll rail's column. At least the right gutter, so the rail owns that
 * strip exactly; never less than a finger, so the thumb stays draggable on a phone
 * where the gutter shrinks to reclaim text width.
 */
export const CHAT_RAIL_WIDTH_VAR = '--chat-rail-width'

/**
 * Diameter (px) of a touch target, used wherever a finger must land on something
 * that a mouse hits comfortably at a smaller size: the rail's column on a coarse
 * pointer, and the hit circle around a rail dot.
 */
export const COARSE_HIT_PX = 24
