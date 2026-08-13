import { globalStyle, keyframes, style } from '@vanilla-extract/css'
import { codeTypography, codeWrap } from '~/styles/codeBlock'
import { CHAT_PAD_LEFT_VAR, CHAT_PAD_RIGHT_VAR, CHAT_RAIL_WIDTH_VAR } from './chatChromeVars'
import { BAND_BORDER_PX } from './chatRowGeometry'
import { shikiDualThemeColors } from './shikiTokenColors.css'
import { toolHeaderActions, toolHeaderTimestamp } from './toolStyles.css'
import { ROW_BLEED_LEFT_VAR } from './widgets/SpanLines.geometry'

/**
 * A band's vertical breathing room: the space between its border line and its
 * first and last line of text. Defined once because two rules must agree on it --
 * the band's own padding, and the top offset that drops the band's absolute
 * toolbar onto the band's first text line.
 */
const BAND_PAD_Y = 'var(--space-3)'

/** Distance from the chat list's own edges to the panel's, on each side. */
const gutterLeft = `var(${CHAT_PAD_LEFT_VAR}, 0px)`
const gutterRight = `var(${CHAT_PAD_RIGHT_VAR}, 0px)`

/**
 * Distance from a CONTENT COLUMN's left edge to the PANEL's left edge: the gutter
 * plus the span rails' reserved width, which only the row knows. Every element
 * that starts a content column publishes ROW_BLEED_LEFT_VAR, so a bleeding child
 * reads the real distance instead of assuming the bare gutter. The fallback
 * covers a content column with no rails beside it.
 */
const contentColumnLeft = `var(${ROW_BLEED_LEFT_VAR}, ${gutterLeft})`

/** Cancel the distance to a panel edge, so a box reaches it on that side. */
export const bleedLeft = `calc(-1 * ${contentColumnLeft})`
export const bleedRight = `calc(-1 * ${gutterRight})`

/**
 * Widen a box that CLIPS its contents (paint containment) so its padding box
 * reaches both panel edges, without moving its content box. Pairs with the
 * negative margins above on whatever bleeds inside it.
 */
export const contentColumnBleed = {
  marginLeft: bleedLeft,
  marginRight: bleedRight,
  paddingLeft: contentColumnLeft,
  paddingRight: gutterRight,
} as const

/**
 * Widen a ROW to the full panel width without moving anything inside it: the
 * negative margins cancel the list gutter, and the equal padding puts the row's
 * CONTENT box back exactly where it was.
 *
 * Both sides read `gutterLeft`, never `contentColumnLeft`. A row sits AT the
 * gutter with no rails to its left, so the two are equal today -- but
 * ROW_BLEED_LEFT_VAR inherits, and a row mounted inside some future content
 * column would inherit a wider value into the margin alone. The cancellation
 * would then stop cancelling, and the row's text would wrap at a width that
 * hidden premeasure never reproduces. One source on both sides makes that
 * unrepresentable rather than merely absent.
 *
 * Two things need this. A row that paints its OWN decoration edge to edge (a
 * band's background and borders) needs only this, because an element's own
 * background and border are not subject to paint containment. A row whose
 * decoration is drawn by a DESCENDANT (a turn-end rule, a user bubble's right
 * side) needs this AND a matching negative margin on that descendant, because
 * paint containment on `virtualRow` and `railedRowContent`
 * (~/components/chat/ChatView.css.ts) clips a descendant to the row's padding
 * box -- which is precisely what this widens.
 */
const rowBleed = {
  marginLeft: `calc(-1 * ${gutterLeft})`,
  marginRight: bleedRight,
  paddingLeft: gutterLeft,
  paddingRight: gutterRight,
} as const

export const messageBubble = style({
  position: 'relative',
  padding: 'var(--space-3) var(--space-4)',
  borderRadius: 'var(--radius-medium)',
  lineHeight: 1.6,
  maxWidth: '85%',
  wordBreak: 'break-word',
})

/**
 * A user message's bubble: an accent card at the end of the line, rounded and
 * outlined on every side, and only as wide as the message needs up to the shared
 * 85% cap.
 *
 * It carries NO bleed of its own. Running its right side off the panel edge is
 * `bubbleFlushRight` below, which only takes effect inside a widened row.
 */
const userBubble = {
  backgroundColor: 'var(--accent)',
  border: '1px solid var(--border)',
  color: 'var(--foreground)',
  alignSelf: 'flex-end',
} as const

export const userMessage = style([messageBubble, userBubble])

const pendingPulse = keyframes({
  '0%, 100%': { opacity: 0.5 },
  '50%': { opacity: 0.85 },
})

/** The same bubble, pulsing while an optimistic local waits for its agent. */
export const userMessagePending = style([messageBubble, {
  ...userBubble,
  'animation': `${pendingPulse} 1.5s ease-in-out infinite`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
      opacity: 0.6,
    },
  },
}])

/**
 * Fallback bubble for an AGENT-source row that is not a band -- in practice an
 * `unknown` shape from a registered provider, which renders as a bare raw-text
 * span and would otherwise float with no container at all. A band is reserved
 * for the kinds messageBandKind lists; anything else the agent emits keeps this
 * bubble so an unclassified row stays visibly a bubble, not chrome-less text.
 */
export const agentFallbackMessage = style([messageBubble, {
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  color: 'var(--foreground)',
  alignSelf: 'flex-start',
}])

/**
 * The band an assistant message or thought paints: a full-bleed gray surface with
 * a line on its top and bottom edge. Applied to the ROW element, not to the
 * bubble, because paint containment would clip a bubble at the gutter, and
 * because a band painted BEHIND the row's content is what lets the span rails and
 * the toolbar sit on the gray.
 *
 * `--row-surface` tells the row's absolute toolbar which color to paint behind
 * itself: one toolbar rule serves both this row family and the meta rows, whose
 * surface is the panel background.
 */
export const bandRow = style({
  ...rowBleed,
  backgroundColor: 'var(--card)',
  borderTop: `${BAND_BORDER_PX}px solid var(--border)`,
  borderBottom: `${BAND_BORDER_PX}px solid var(--border)`,
  vars: {
    '--row-surface': 'var(--card)',
  },
})

/**
 * A row that paints nothing itself and only widens, so that a DESCENDANT can
 * reach a panel edge. Used by a turn-end divider (its rule runs to both edges,
 * see the globalStyle beside `resultDivider`) and by a user message row (its
 * bubble's right side runs to the right edge, see `bubbleFlushRight`).
 */
export const bleedRow = style(rowBleed)

/**
 * Marks a BUBBLE that runs its right side off the panel edge.
 *
 * The class carries nothing by itself. The declarations that move the bubble live
 * in the rule below, scoped to `bleedRow`, so a marked bubble in a row that was
 * never widened produces no bleed at all -- rather than one that paint
 * containment silently clips at the gutter. The pairing that the row chrome and
 * the bubble class have to agree on is expressed in the selector instead of being
 * left to two predicates that happen to overlap. Same construction as the turn-end
 * rule below.
 */
export const bubbleFlushRight = style({})

globalStyle(`${bleedRow} .${bubbleFlushRight}`, {
  // Cancel the gutter, then drop the right border and the right corners: a
  // rounded, outlined corner flush against a panel edge reads as a mistake.
  marginRight: bleedRight,
  borderRightWidth: 0,
  borderTopRightRadius: 0,
  borderBottomRightRadius: 0,
})

/**
 * A thought's band. It carries a dashed line instead of a solid one. Dashed is
 * the chat's established mark for a de-emphasized surface -- systemMessage,
 * planExecutionMessage and hiddenMessageJson all use it, as the thought bubble
 * itself did before it became a band.
 *
 * A modifier, applied BESIDE `bandRow` rather than composed into it, because both
 * classes are read as single tokens: `messageRowChromeClass` joins them, and the
 * tests assert each one with `classList.contains`.
 */
export const bandRowThought = style({
  borderTopStyle: 'dashed',
  borderBottomStyle: 'dashed',
})

/**
 * The content inside a band. It carries NO background, border or radius -- the
 * band behind it does (see bandRow) -- and it stretches to the row's full
 * content width like a tool message rather than capping at a bubble's 85%.
 *
 * The vertical padding lives here, not on bandRow, so the span rails (a flex
 * sibling of this element's wrapper, stretched by the row) span the whole gray
 * instead of stopping short of it at every band boundary.
 */
export const bandMessage = style({
  position: 'relative',
  alignSelf: 'stretch',
  flex: 1,
  minWidth: 0,
  paddingTop: BAND_PAD_Y,
  paddingBottom: BAND_PAD_Y,
  lineHeight: 1.6,
  wordBreak: 'break-word',
  color: 'var(--foreground)',
})

export const planExecutionMessage = style([messageBubble, {
  backgroundColor: 'var(--accent)',
  border: '1px dashed var(--border)',
  color: 'var(--foreground)',
  alignSelf: 'flex-end',
}])

export const thinkingHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  color: 'var(--muted-foreground)',
  cursor: 'pointer',
  userSelect: 'none',
})

export const thinkingChevron = style({
  'flexShrink': 0,
  'transition': 'transform 150ms cubic-bezier(0.4, 0, 0.2, 1)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const thinkingChevronExpanded = style({
  transform: 'rotate(90deg)',
})

export const thinkingContent = style({
  marginTop: 'var(--space-2)',
})

export const systemMessage = style([messageBubble, {
  backgroundColor: 'transparent',
  border: '1px dashed var(--border)',
  color: 'var(--muted-foreground)',
  alignSelf: 'center',
  fontSize: 'var(--text-7)',
}])

globalStyle(`${systemMessage} pre`, {
  whiteSpace: 'pre-wrap',
  margin: 0,
})

export const metaMessage = style({
  alignSelf: 'stretch',
  minWidth: 0,
})

export const resultDivider = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  selectors: {
    '&::before': {
      content: '""',
      flex: 1,
      height: '1px',
      background: 'var(--border)',
    },
    '&::after': {
      content: '""',
      flex: 1,
      height: '1px',
      background: 'var(--border)',
    },
  },
})

/**
 * Run a turn-end divider's rule to both panel edges. Scoped to `bleedRow`, and
 * NOT put on `resultDivider` itself, because the same class also draws the
 * compaction-boundary rule inside a CENTERED bubble (NotificationDivider in
 * ~/components/chat/notificationRenderers.tsx). Bleeding there would push the
 * rule straight out of that bubble's dashed border.
 */
globalStyle(`${bleedRow} .${resultDivider}`, {
  marginLeft: bleedLeft,
  marginRight: bleedRight,
})

// Error detail text shown below the result divider for execution errors
export const resultErrorDetail = style({
  margin: 0,
  padding: '0 var(--space-3)',
  fontSize: 'var(--text-7)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-word',
  color: 'var(--muted-foreground)',
})

// Hidden message rendered as raw JSON (developer mode)
export const hiddenMessageJson = style({
  ...codeTypography,
  ...codeWrap,
  margin: 0,
  padding: 'var(--space-2) var(--space-3)',
  color: 'var(--muted-foreground)',
  backgroundColor: 'var(--card)',
  border: '1px dashed var(--border)',
  borderRadius: 'var(--radius-small)',
  maxHeight: '300px',
  overflow: 'auto',
})

// JSON renders as token <span>s (data-shiki-token) directly inside this wrapper,
// which already owns the mono font + pre-wrap + padding/border/scroll. The spans
// pick up dual-theme colors via CSS vars.
shikiDualThemeColors(`${hiddenMessageJson} span[data-shiki-token]`, { bg: true })

// Control response message (compact)
export const controlResponseMessage = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  color: 'var(--foreground)',
  fontSize: 'var(--text-7)',
  alignSelf: 'stretch',
})

// A control-response LABEL row (Allow / Approved / "Task: Build\nEnv: Dev"). `pre-line` preserves
// the `\n` line breaks a multi-question answer joins its lines with, so they render one per line
// instead of collapsing to a single run.
export const controlResponseLabel = style({
  whiteSpace: 'pre-line',
})

// Base styles for message row layout
const messageRowBase = {
  display: 'flex',
  alignItems: 'flex-start',
  gap: 'var(--space-1)',
  alignSelf: 'stretch',
  maxWidth: '100%',
} as const

// Flex row wrapping a message bubble + right-aligned ToolHeaderActions outside the bubble
export const messageRow = style(messageRowBase)

// Right-aligned variant for user message bubbles
export const messageRowEnd = style({
  ...messageRowBase,
  justifyContent: 'flex-end',
})

// Centered variant for status/notification messages
export const messageRowCenter = style({
  ...messageRowBase,
  justifyContent: 'center',
  position: 'relative',
})

// Extra vertical spacing for user and notification message rows. A band row is
// deliberately absent: its own vertical padding (bandMessage) provides the
// breathing room, and a margin here would put transparent space between two
// bands that must touch.
globalStyle(`${messageRowEnd}, ${messageRowCenter}`, {
  marginTop: 'var(--space-1)',
  marginBottom: 'var(--space-1)',
})

// Inside messageRow, stretch meta messages (tools, result dividers) to fill available space
globalStyle(`${messageRow} > .${metaMessage}`, {
  flex: 1,
  alignSelf: 'auto',
})

// A meta row (tool, divider) and a band row both give their whole width to the
// content, so the actions float over it instead of taking layout space.
globalStyle(`${messageRow}:has(> .${metaMessage}), ${messageRow}:has(> .${bandMessage})`, {
  position: 'relative',
})

/**
 * How far the floating actions sit inside the content column's right edge: exactly
 * the amount by which the scroll rail's column overhangs the gutter it fills.
 *
 * The rail is normally the gutter and nothing more, so this is 0 and the actions
 * sit flush at the edge. Where the rail takes a floor -- a 4px phone gutter, or a
 * coarse pointer that needs a whole finger -- the column reaches into the message
 * text, and the rail wins every pointer event in its own box because it overlays
 * the row from OUTSIDE that row's stacking context. Anything under the overhang is
 * unreachable. Moving the actions clear of it by the same two properties the rail
 * is sized from keeps them reachable at every width, with no breakpoint of its own
 * to keep in step.
 */
const railOverhang = `calc(var(${CHAT_RAIL_WIDTH_VAR}, ${gutterRight}) - ${gutterRight})`

// A meta row (tool, divider), a band row and a notification row all give their
// whole width to the content, so the actions float over it at the same right edge
// instead of taking layout space.
globalStyle(`${messageRow}:has(> .${metaMessage}) > .${toolHeaderActions}, ${messageRow}:has(> .${bandMessage}) > .${toolHeaderActions}, ${messageRowCenter} > .${toolHeaderActions}`, {
  position: 'absolute',
  right: railOverhang,
  marginLeft: 0,
  // The surface the actions must occlude: the panel background on a meta row or a
  // notification row, the band's gray on a band row (bandRow sets --row-surface).
  background: 'var(--row-surface, var(--background))',
  borderRadius: 'var(--radius-small)',
  paddingLeft: 'var(--space-1)',
})

// A band's content starts below its own top padding, so drop the actions by the
// same amount to put them on the band's first text line.
globalStyle(`${messageRow}:has(> .${bandMessage}) > .${toolHeaderActions}`, {
  top: BAND_PAD_Y,
})

// Inside messageRowEnd, place actions to the left of the bubble in a 2-column grid (mirrored via RTL)
globalStyle(`${messageRowEnd} > .${toolHeaderActions}`, {
  order: -1,
  paddingRight: 'var(--space-1)',
  paddingTop: 'var(--space-1)',
  paddingBottom: 'var(--space-1)',
  display: 'grid',
  gridTemplateColumns: 'auto auto',
  direction: 'rtl',
})

// Reset direction on children so text inside buttons renders LTR
globalStyle(`${messageRowEnd} > .${toolHeaderActions} > *`, {
  direction: 'ltr',
})

// Add right padding to timestamps in user grid (mirrored) so they align with the icon button below
globalStyle(`${messageRowEnd} > .${toolHeaderActions} .${toolHeaderTimestamp}`, {
  paddingRight: 'var(--space-1)',
})

// When hovering a message row, reveal the actions
globalStyle(`${messageRow}:hover .${toolHeaderActions}, ${messageRowEnd}:hover .${toolHeaderActions}, ${messageRowCenter}:hover .${toolHeaderActions}`, {
  opacity: 1,
})

globalStyle(`${messageBubble} code, ${bandMessage} code`, {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

globalStyle(`${messageBubble} pre, ${bandMessage} pre`, {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

// Attachment list shown inside user message bubbles in chat history
export const attachmentList = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  gap: '2px',
  fontSize: 'var(--text-8)',
  marginBottom: 'var(--space-2)',
})

export const attachmentItem = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  color: 'var(--muted-foreground)',
})
