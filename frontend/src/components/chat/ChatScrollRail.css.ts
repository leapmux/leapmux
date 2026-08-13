import { globalStyle, style } from '@vanilla-extract/css'
import { floatingCardSurface } from '~/styles/popover.css'
import { CHAT_RAIL_WIDTH_VAR, COARSE_HIT_PX } from './chatChromeVars'

// Fixed px here are scrollbar-like dimensions (rail/thumb/dot widths, thumb radius),
// NOT spacing-scale values -- see CLAUDE.md. The top/bottom insets use the spacing scale.

/**
 * Max height of the preview card (px): the point at which a long preview stops growing the card
 * and starts scrolling inside it.
 *
 * The cap only. The vertical clamp in `cardTopPx` (`./chatDotPreview.ts`) deliberately does NOT
 * read this: it clamps against the card's MEASURED height, because most previews are far shorter
 * than the cap and clamping them as though they were 200px tall pushed a one-line card up to
 * ~90px away from the dot it describes.
 */
const PREVIEW_CARD_MAX_H_PX = 200

/** Width of the muted vertical track line. */
const TRACK_WIDTH_PX = 2
/** Thumb width, matching the app's 8px native scrollbar thumb. */
const THUMB_WIDTH_PX = 8

/**
 * The rail's box comes from a token the chat root publishes
 * (`~/components/chat/ChatView.css.ts`), because a message row's floating toolbar
 * needs the same number to sit clear of this column. The token travels as a
 * custom property rather than an import: `ChatView.css.ts` already imports
 * `~/components/chat/messageStyles.css.ts`, and the toolbar rules live there, so
 * the value has to reach two modules that cannot both import each other. The
 * NAMES are shared, from the import-free leaf `~/components/chat/chatChromeVars.ts`.
 *
 * The column is the gutter. That one decision does three things at once:
 * everything the rail draws sits centred in it, so the track, thumb and dots run
 * down the gutter's middle; the box normally stops at the message column, so it
 * does not cover a row's floating action buttons; and the hover target is the
 * whole gutter rather than the ~10px strip that was hard to hit.
 *
 * The second point is the one that matters most. The rail overlays a row from
 * OUTSIDE its stacking context and stays hit-testable while faded, so every
 * pointer event inside the rail's box goes to the rail, and no z-index or
 * pointer-events arrangement inside the row changes that. A box that stops at the
 * gutter cannot receive an event meant for a row's action buttons.
 *
 * Two floors break that rule on purpose, because a 4px phone gutter and a coarse
 * pointer both need a wider press target than the gutter gives. The column then
 * DOES overhang the message text, and the row's floating actions are inset by
 * exactly that overhang to stay clear of it -- see the toolbar rules in
 * `~/components/chat/messageStyles.css.ts`, which read the same two properties.
 */
const RAIL_WIDTH = `var(${CHAT_RAIL_WIDTH_VAR})`

/**
 * Opacity of the rail's own surface while it is shown.
 *
 * Painted in `--background` -- the PANEL colour, not the message surface -- so
 * the strip reads as a channel cut out of the content: it lightens the column in
 * the light theme and darkens it in the dark one, from the same declaration.
 * Partial, so the content beneath still shows through and the strip does not
 * read as an opaque bar. The whole rail fades with `railIdle`, so this needs no
 * shown/hidden branch of its own.
 */
const RAIL_SURFACE_ALPHA = 0.7

export const rail = style({
  'position': 'absolute',
  'top': 'var(--space-2)',
  'bottom': 'var(--space-2)',
  'right': 0,
  'width': RAIL_WIDTH,
  'backgroundColor': `color-mix(in srgb, var(--background) ${RAIL_SURFACE_ALPHA * 100}%, transparent)`,
  // Round the surface off the same way as the loading pills and the scroll-to-bottom
  // button: the base radius token, NOT a pill. All of them float over this one viewport,
  // so they should read as one set. (The corner this replaced was twice the TRACK's width,
  // which is a measurement of the line inside the strip, not of the strip.)
  'borderRadius': 'var(--radius-medium)',
  'zIndex': 10,
  // Interactive (unlike the pointer-events:none loading pills): track clicks + thumb drag.
  'cursor': 'pointer',
  // Never capture text selection while dragging the thumb.
  'userSelect': 'none',
  'touchAction': 'none',
  // The fade railIdle drives. The transition animates both directions (show + hide) and is
  // scoped to (prefers-reduced-motion: no-preference) rather than overridden by a competing
  // (prefers-reduced-motion: reduce) { transition: none } block. The two blocks set the same
  // property at equal specificity, so winning the tie by source order is fragile (a key reorder
  // silently re-enables the fade under reduced-motion). Guarding it behind `no-preference`
  // makes reduced-motion the default and the fade opt-in, with no tie to break.
  '@media': {
    '(prefers-reduced-motion: no-preference)': {
      transition: 'opacity var(--transition)',
    },
  },
})

/**
 * Idle (auto-hide) state, applied whenever the reader is outside the host's scroll-activity
 * window. The WHOLE rail fades as one -- track, thumb, and teal jump dots -- because they are
 * children of this element; fading them separately would let the dots read as a second,
 * unrelated affordance mid-transition.
 *
 * The element is never unmounted (see ChatScrollRailProps.scrollActive), so `opacity` is the
 * only safe lever. `visibility: hidden` or `display: none` would drop it out of the hit-test
 * tree and could disturb the height its own ResizeObserver measures.
 *
 * `pointer-events: none` is COARSE-ONLY, deliberately:
 *   - on touch the idle rail must go inert. The column takes a finger-sized floor there, so on
 *     a phone, whose right gutter is 4px, this strip overlaps 20px of message content. An
 *     invisible strip that swallowed taps into a track-click jump would be a trap.
 *   - on a FINE pointer the faded rail STAYS hit-testable so a pointer over it relights it (see
 *     ChatScrollRail.onPointerEnter, and onPointerMove for the strip that appears under an
 *     already-stationary cursor), but a CLICK on the faded rail is rejected in the pointer
 *     handler -- you can't click what you can't see. So a mouse reaches the rail by scrolling or
 *     by moving onto the strip, which relights it, first.
 *
 * A universal `pointer-events: none` cannot protect what the column overhangs. The relight has
 * to come from a pointer event on the strip itself, so an inert strip never lights again from
 * the pointer, and a strip that stays live takes the hover from whatever it covers. Neither
 * setting helps, which is why the overhang is removed at the source instead: the column is the
 * gutter, and the row's actions are inset by the little that the two floors add to it.
 *
 * `opacity: 0` is universal: every screen/pointer fades the idle rail the same way. There is
 * deliberately NO `:hover { opacity: 1 }` shortcut. That would make the rail LOOK visible while
 * the press guard still read it as faded, and the guard would reject a click on something that
 * appeared visible. A parked cursor DOES hold the rail lit, but through the `hovered` signal
 * that `idle()` folds in (ChatScrollRail.tsx), so the look and the guard move together.
 */
export const railIdle = style({
  'opacity': 0,
  '@media': {
    '(pointer: coarse)': {
      pointerEvents: 'none',
    },
  },
})

// The thumb (cursor: grab) and dots (cursor: pointer) are children of the faded rail, and cursor
// is independent of opacity -- so without this, a fine pointer crossing the invisible strip sees a
// grab/pointer caret over blank space. Reset every descendant to the default while the rail is
// faded. `& *` targets the thumb, dots, and track; the strip itself keeps cursor: pointer (harmless:
// the whole faded strip is a uniform invisible region, and a press on it only reveals the rail).
globalStyle(`${railIdle} *`, {
  cursor: 'auto',
})

/**
 * The muted vertical track line, centred in the rail -- and so, because the rail
 * fills the gutter exactly, down the gutter's middle. The thumb and the dots
 * share that axis. Non-interactive (clicks fall to rail).
 */
export const track = style({
  position: 'absolute',
  top: 0,
  bottom: 0,
  left: '50%',
  transform: 'translateX(-50%)',
  width: `${TRACK_WIDTH_PX}px`,
  borderRadius: `${TRACK_WIDTH_PX / 2}px`,
  backgroundColor: 'var(--border)',
  pointerEvents: 'none',
})

/** The scrollbar thumb, positioned/sized in seq space. Uses the app's scrollbar tokens. */
export const thumb = style({
  position: 'absolute',
  left: '50%',
  transform: 'translateX(-50%)',
  width: `${THUMB_WIDTH_PX}px`,
  borderRadius: '4px',
  backgroundColor: 'var(--scrollbar-thumb)',
  zIndex: 1,
  cursor: 'grab',
  transition: 'background-color 0.12s ease',
  selectors: {
    '&:hover': {
      backgroundColor: 'var(--scrollbar-thumb-hover)',
    },
  },
})

export const thumbDragging = style({
  backgroundColor: 'var(--scrollbar-thumb-hover)',
  cursor: 'grabbing',
})

/**
 * A teal jump dot marking a user input / control response, centered on its seq band.
 * Rendered ABOVE the thumb (higher z-index) so an overlapping thumb never hides it --
 * the dot is prioritized, per the design.
 */
export const dot = style({
  position: 'absolute',
  left: '50%',
  width: '6px',
  height: '6px',
  borderRadius: '50%',
  backgroundColor: 'var(--primary)',
  // Center the dot on its fraction (top set inline) and horizontally.
  transform: 'translate(-50%, -50%)',
  zIndex: 2,
  cursor: 'pointer',
  padding: 0,
  border: 'none',
  // A thin ring in the panel background separates adjacent/overlapping dots so
  // a cluster reads as distinct dots rather than one blob. A box-shadow ring
  // (not a border) keeps the 6px colored fill intact instead of eating into it.
  boxShadow: '0 0 0 1px var(--background)',
  transition: 'background-color 0.12s ease',
  selectors: {
    // Recolor on hover so the dot the preview card describes is visually distinct.
    '&:hover': {
      backgroundColor: 'var(--accent)',
    },
  },
})

/**
 * Diameter (px) of the coarse-pointer hit circle around a dot. Exported because the scrub-preview
 * range in `./chatDotPreview.ts` is derived from it rather than hand-synced: a finger presses
 * anywhere inside this circle, and the preview card that press opens is resolved by distance from
 * the dot's rail-Y, so a range narrower than this circle's radius leaves a rim of the hit area
 * that jumps but shows no preview. Same reason PREVIEW_CARD_MAX_H_PX is exported above.
 *
 * The finger size itself comes from the shared constant, which the rail's own column also floors
 * at on a coarse pointer -- so a dot and the track around it take the same press target.
 */
export const DOT_COARSE_HIT_PX = COARSE_HIT_PX

// Coarse-pointer (touch) hit expander: a transparent circle centred on the 6px dot, so a finger
// tap within range still hits the button. A pseudo-element leaves the dot's visual fill + ring at
// 6px (unlike padding, which would enlarge them or the box-shadow).
globalStyle(`${dot}::before`, {
  '@media': {
    '(pointer: coarse)': {
      content: '',
      position: 'absolute',
      top: '50%',
      left: '50%',
      transform: 'translate(-50%, -50%)',
      width: `${DOT_COARSE_HIT_PX}px`,
      height: `${DOT_COARSE_HIT_PX}px`,
      borderRadius: '50%',
    },
  },
})

/**
 * A dot standing for MULTIPLE marks collapsed to one rail pixel (a cluster). An extra
 * outer ring in the primary colour distinguishes it from a single-mark dot, so a dense
 * band reads as "several messages here" rather than one. The inner --background ring
 * (from `dot`) still separates it from neighbours.
 */
export const dotCluster = style({
  boxShadow: '0 0 0 1px var(--background), 0 0 0 2.5px var(--primary)',
})

/** Small muted header in a cluster's preview card: how many messages it stands for. */
export const dotPreviewCount = style({
  fontSize: 'var(--text-8)',
  opacity: 0.7,
  marginBottom: 'var(--space-1)',
})

/** Placeholder line shown in the dot's preview card while its preview is still in flight. */
export const dotPreviewLoading = style({
  opacity: 0.7,
  fontStyle: 'italic',
})

/**
 * Wraps the markdown-rendered preview inside the dot's preview card. The markdown renderer
 * emits block elements (paragraphs, blockquotes, lists) with their own vertical
 * margins; strip the outer ones so the preview sits flush against the card's inset.
 */
export const dotPreviewMarkdown = style({})

// Child selectors must be globalStyle in vanilla-extract (style() selectors can only
// target the element itself). The markdown body is rendered by MarkdownText inside this
// wrapper, so reach its first/last block to drop the outer margins.
globalStyle(`${dotPreviewMarkdown} > * > :first-child`, { marginTop: 0 })
globalStyle(`${dotPreviewMarkdown} > * > :last-child`, { marginBottom: 0 })

/**
 * The single live preview card shown to the LEFT of the rail for the ACTIVE dot -- whether
 * it's hovered/focused or under the dragging thumb (scrub). One element for both cases, so a
 * hover and a scrub can never show two cards at once. Its top is set inline (clamped to
 * the rail so it never clips against the overflow-hidden wrapper); translateY(-50%) centres
 * it on that Y.
 *
 * The card is INTERACTIVE on a fine pointer: the reader moves onto it to select or to scroll its
 * text, and the close delay in createDotPreview buys them the time to get there. It is inert on a
 * COARSE pointer, for the same reason railIdle goes inert there. A touch has no hover, so the card
 * outlives by that delay the gesture that opened it, and a 280px interactive card lying over the
 * message list would swallow the reader's next tap. A finger reads the card during the scrub and
 * dismisses it by lifting; it never moves onto it. DotPreviewCard keeps the card's own pointer and
 * wheel traffic off the rail's handlers, which is what makes the fine-pointer case safe.
 *
 * The cost of that reach is that an open card is a hit target: on a fine pointer it covers up to
 * 280x200px of the message list, and a click there lands on the card rather than on the message
 * under it. That is the trade a reachable card makes -- the card cannot both accept the reader's
 * selection drag and pass their click through -- and the close delay bounds it: move off the card
 * and the region accepts clicks again a moment later.
 *
 * Its whole surface -- Oat's card fill, border and radius, the compact inset, and the lift that
 * separates it from the transcript -- comes from the shared `floatingCardSurface` in
 * `~/styles/popover.css.ts`, the same class `~/components/common/Tooltip.css.ts` carries. Every
 * one of those declarations used to be written out here, and the copies had already drifted: this
 * card filled `var(--background)` while every other floating surface filled `var(--card)`, so the
 * one card that lay OVER the reader's work was the one painted the page's own colour.
 */
export const previewCard = style([floatingCardSurface, {
  'position': 'absolute',
  // Flush against the rail's left edge. The rail is a wide hit column whose
  // visuals sit centred in it, so any gap here is added to the ~10px the column
  // already puts between its left edge and the dot the card describes -- and the
  // card drifts away from the thing it points at.
  'right': '100%',
  'transform': 'translateY(-50%)',
  'width': 'max-content',
  'maxWidth': '280px',
  'maxHeight': `${PREVIEW_CARD_MAX_H_PX}px`,
  'overflowY': 'auto',
  // On Oat's type scale, and on the same step as the app's other floating surface. The 0.8125rem
  // this used to specify sat between two steps of that scale and matched nothing else.
  'fontSize': 'var(--text-8)',
  'zIndex': 20,
  'pointerEvents': 'auto',
  // The rail sets user-select:none (so a thumb drag never selects text) and cursor:pointer, and
  // both inherit into this card. Undo them here: the card's text is meant to be read, selected,
  // and copied, and the card itself is not a control.
  'userSelect': 'text',
  'cursor': 'auto',
  '@media': {
    '(pointer: coarse)': {
      pointerEvents: 'none',
    },
  },
}])
