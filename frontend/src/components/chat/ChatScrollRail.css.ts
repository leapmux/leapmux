import { globalStyle, style } from '@vanilla-extract/css'

// Fixed px here are scrollbar-like dimensions (rail/thumb/dot widths, thumb radius),
// NOT spacing-scale values -- see CLAUDE.md. The top/bottom insets use the spacing scale.

/**
 * Max height of the preview popover (px). Exported so ChatScrollRail's vertical clamp (which
 * keeps a near-edge popover from clipping against the overflow-hidden wrapper) reads the SAME
 * value the CSS enforces, rather than a hand-synced literal that could drift from this one.
 */
export const PREVIEW_POPOVER_MAX_H_PX = 200

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
    // Recolor on hover so the dot the tooltip describes is visually distinct.
    '&:hover': {
      backgroundColor: 'var(--accent)',
    },
  },
})

// Coarse-pointer (touch) hit expander: a transparent ~24px circle centred on the 6px dot,
// so a finger tap within range still hits the button. A pseudo-element leaves the dot's
// visual fill + ring at 6px (unlike padding, which would enlarge them or the box-shadow).
globalStyle(`${dot}::before`, {
  '@media': {
    '(pointer: coarse)': {
      content: '',
      position: 'absolute',
      top: '50%',
      left: '50%',
      transform: 'translate(-50%, -50%)',
      width: '24px',
      height: '24px',
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

/** Small muted header in a cluster's tooltip: how many messages it stands for. */
export const dotPreviewCount = style({
  fontSize: '0.75rem',
  opacity: 0.7,
  marginBottom: 'var(--space-1)',
})

/** Placeholder line shown in the dot tooltip while its preview is being fetched. */
export const dotPreviewLoading = style({
  opacity: 0.7,
  fontStyle: 'italic',
})

/**
 * Wraps the markdown-rendered preview inside the dot tooltip. The markdown renderer
 * emits block elements (paragraphs, blockquotes, lists) with their own vertical
 * margins; strip the outer ones so the preview sits flush against the tooltip padding.
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
 * hover and a scrub can never show two popovers at once. Its top is set inline (clamped to
 * the rail so it never clips against the overflow-hidden wrapper); translateY(-50%) centres
 * it on that Y. Non-interactive so it never intercepts a drag or a click.
 */
export const previewPopover = style({
  position: 'absolute',
  right: 'calc(100% + var(--space-2))',
  transform: 'translateY(-50%)',
  width: 'max-content',
  maxWidth: '280px',
  maxHeight: `${PREVIEW_POPOVER_MAX_H_PX}px`,
  overflowY: 'auto',
  padding: 'var(--space-2) var(--space-3)',
  borderRadius: '6px',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  boxShadow: '0 2px 8px rgba(0, 0, 0, 0.15)',
  fontSize: '0.8125rem',
  lineHeight: 1.4,
  color: 'var(--text)',
  pointerEvents: 'none',
  zIndex: 20,
})
