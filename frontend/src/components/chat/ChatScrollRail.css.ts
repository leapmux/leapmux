import { globalStyle, style } from '@vanilla-extract/css'
import { floatingCardSurface } from '~/styles/popover.css'

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

/** Width of the rail overlay column (a touch wider than the 8px native scrollbar). */
const RAIL_WIDTH_PX = 10
/**
 * Wider interactive column on COARSE pointers (touch): a finger can't hit a 10px strip, so
 * the hit area grows to a touch-friendlier size while the thin visuals (track/thumb/dots)
 * stay their fine-pointer widths, just centred in the wider column. Below breakpoints.sm the
 * list's right gutter shrinks to reclaim text width, so this strip now overlaps roughly 20px
 * of message content instead of 8px. That is acceptable ONLY because the rail is
 * pointer-events:none while idle (see railIdle), which is the large majority of the time.
 * Widening it further, or dropping the idle inertness, starts swallowing message taps into
 * track-click jumps.
 */
const RAIL_WIDTH_COARSE_PX = 22
/** Thumb width, matching the app's 8px native scrollbar thumb. */
const THUMB_WIDTH_PX = 8

export const rail = style({
  'position': 'absolute',
  'top': 'var(--space-2)',
  'bottom': 'var(--space-2)',
  'right': '2px',
  'width': `${RAIL_WIDTH_PX}px`,
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
    '(pointer: coarse)': {
      width: `${RAIL_WIDTH_COARSE_PX}px`,
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
 *   - on touch the idle rail must go inert, because below breakpoints.sm the list's right
 *     gutter shrinks and this strip then overlaps ~20px of message content. An invisible
 *     strip that swallowed taps into a track-click jump would be a trap.
 *   - on a FINE pointer the faded rail STAYS hit-testable so a `pointermove` over it relights
 *     it (see ChatScrollRail.onPointerMove -> onActivity), but a CLICK on the faded rail is
 *     rejected in the pointer handler -- you can't click what you can't see. So a mouse
 *     reaches the rail by scrolling or by moving onto the strip (which relights it) first.
 *
 * `opacity: 0` is universal: every screen/pointer fades the idle rail the same way. There is
 * deliberately NO `:hover { opacity: 1 }` shortcut: that would make the rail LOOK visible
 * under a parked cursor while the activity window is still closed, so the click guard would
 * reject a click on something that appeared visible. Visibility stays driven solely by the
 * activity window, which carries its own idle timeout so a parked cursor cannot pin it lit.
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

/** The muted vertical track line, centered in the rail. Non-interactive (clicks fall to rail). */
export const track = style({
  position: 'absolute',
  top: 0,
  bottom: 0,
  left: '50%',
  transform: 'translateX(-50%)',
  width: '2px',
  borderRadius: '1px',
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
 */
export const DOT_COARSE_HIT_PX = 24

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
  'right': 'calc(100% + var(--space-2))',
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
