import { globalStyle, style } from '@vanilla-extract/css'
import { todoList } from '~/components/todo/TodoList.css'
import { codeTypography, codeWrap } from '~/styles/codeBlock'
import { clippedText, controlReset } from '~/styles/shared.css'
import { codeSurface } from './shikiTokenColors.css'
import { LINE_THICKNESS, TOOL_BODY_INDENT } from './widgets/SpanLines.geometry'

// Tool use/result messages - document-style, no bubble
export const toolMessage = style({
  alignSelf: 'stretch',
  fontSize: 'var(--text-7)',
  lineHeight: 1.6,
})

// Tool use header: "» ToolName(...)"
//
// The items share ONE baseline, so the title and the smaller text beside it --
// the running badge at var(--text-8) -- sit on the same line of type. Under
// flex-start each item keeps its own line box instead, and `line-height: 1.6`
// is a number, so a smaller font-size gives a shorter box: the badge's text
// then floated 2px above the title's.
//
// A multi-line title still keeps the icon and the actions on the first line,
// because baseline alignment uses an item's FIRST baseline. Those two opt out
// of it below. Neither has a text baseline to share -- each is a flex box that
// centres its content in a box of one line -- and a flex box with no
// baseline-aligned child synthesizes one from its border box, which makes every
// header 4px taller.
export const toolUseHeader = style({
  display: 'flex',
  alignItems: 'baseline',
  gap: 'var(--space-1)',
  color: 'var(--muted-foreground)',
})

// Icon styling — also used on the wrapper <span> so it acts as a line-height
// box, keeping the icon vertically centred on the first text line. The box is
// top-aligned by the globalStyle below, which reaches only the copy inside a
// tool header; `thinkingHeader` takes this class as well.
export const toolUseIcon = style({
  color: 'var(--muted-foreground)',
  height: '1lh',
  alignItems: 'center',
})

// Tool result content area (markdown)
export const toolResultContent = style({
  color: 'var(--foreground)',
})

// Tool result content as preformatted text (for Bash, Grep, Read output)
export const toolResultContentPre = style({
  ...codeTypography,
  ...codeWrap,
  color: 'var(--foreground)',
})

export const commandStreamContainer = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const commandStreamInteraction = style([
  toolResultContentPre,
  {
    color: 'var(--muted-foreground)',
    paddingLeft: 'var(--space-2)',
    borderLeft: '2px solid var(--border)',
  },
])

// Tool result error message (subtle styling for transient, auto-recovered errors)
export const toolResultError = style({
  color: 'var(--muted-foreground)',
})

// Tool result content with ANSI escape sequence rendering (for Bash output)
export const toolResultContentAnsi = style({
  ...codeTypography,
  ...codeWrap,
  // Base color for un-tokenized text nodes -- the raw-text fallback shown while the
  // async token worker is in flight / paused / over the size cap (TokenizedCode's
  // `fallback={props.code}`), and the JSON tool-result body before tokens land.
  // Without this the fallback inherits the browser-default black, which is
  // near-invisible on the dark theme. Shiki token spans (`span[data-shiki-token]` /
  // `pre.shiki span`) set their own color via higher-specificity globals and still
  // override this. Matches toolResultContentPre, which the JSON null state used before.
  color: 'var(--foreground)',
})

// Override Shiki's default <pre> styling inside ANSI tool result
globalStyle(`${toolResultContentAnsi} pre.shiki`, {
  margin: 0,
  padding: 0,
  border: 'none',
  background: 'none',
  backgroundColor: 'transparent',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  fontSize: 'inherit',
  fontFamily: 'inherit',
  lineHeight: 'inherit',
})

globalStyle(`${toolResultContentAnsi} pre.shiki code`, {
  padding: 0,
  background: 'none',
  backgroundColor: 'transparent',
  fontSize: 'inherit',
  fontFamily: 'inherit',
})

// Shiki dual-theme support for ANSI-rendered spans, and for JSON tool results that
// render as token <span>s (data-shiki-token) in this same wrapper.
codeSurface(toolResultContentAnsi, 'page', [
  { suffix: ' pre.shiki span', bg: true },
  { suffix: ' span[data-shiki-token]', bg: true },
])

// Collapsed tool results: max 3rem height with fade-out gradient
export const toolResultCollapsed = style({
  maxHeight: '3.6rem',
  overflow: 'hidden',
  WebkitMaskImage: 'linear-gradient(to bottom, black calc(100% - 1.5em), transparent)',
  maskImage: 'linear-gradient(to bottom, black calc(100% - 1.5em), transparent)',
})

// Cap heading font sizes inside collapsed markdown previews
globalStyle(`${toolResultCollapsed} h1, ${toolResultCollapsed} h2, ${toolResultCollapsed} h3, ${toolResultCollapsed} h4, ${toolResultCollapsed} h5, ${toolResultCollapsed} h6`, {
  fontSize: 'inherit',
  margin: 0,
})

// Prompt label shown above WebFetch tool result
export const toolResultPrompt = style({
  color: 'var(--muted-foreground)',
  marginBottom: 'var(--space-1)',
})

// Tool summary line (monospace). Same code typography as the expanded body
// (toolResultContentAnsi) so the SAME command keeps its size + line height when toggled
// between the collapsed summary and the expanded body.
export const toolInputSummary = style({
  ...codeTypography,
  ...codeWrap,
  color: 'var(--muted-foreground)',
})

// Collapsed command input summaries show the first three visual rows. This is
// intentionally a visual row cap (not hard-line truncation) so a very long
// single-line command is clipped correctly after wrapping. 4.5em == 3 rows at the
// 1.5 line height above (kept in sync with it).
export const commandInputCollapsed = style({
  maxHeight: '4.5em',
  overflow: 'hidden',
})

export const commandInputCollapsedFade = style({
  WebkitMaskImage: 'linear-gradient(to bottom, black calc(100% - 1.5em), transparent)',
  maskImage: 'linear-gradient(to bottom, black calc(100% - 1.5em), transparent)',
})

// Override Shiki's default <pre> styling inside tool input summary (for Bash highlighting)
globalStyle(`${toolInputSummary} pre.shiki`, {
  margin: 0,
  padding: 0,
  border: 'none',
  background: 'none',
  backgroundColor: 'transparent',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  fontSize: 'inherit',
  fontFamily: 'inherit',
  lineHeight: 'inherit',
})

globalStyle(`${toolInputSummary} pre.shiki code`, {
  padding: 0,
  background: 'none',
  backgroundColor: 'transparent',
  fontSize: 'inherit',
  fontFamily: 'inherit',
})

codeSurface(toolInputSummary, 'page', [
  { suffix: ' pre.shiki span', bg: true },
  { suffix: ' span[data-shiki-token]', bg: true },
])

// Tool input detail text (natural language: descriptions, URLs, queries)
export const toolInputText = style([clippedText, {
  color: 'var(--foreground)',
}])

// Tool input code text (commands, patterns — monospaced)
export const toolInputCode = style([clippedText, {
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
}])

// File path display in tool messages
export const toolInputPath = style({
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

// Tool header actions area (right-aligned group for Code + ThreadExpander buttons)
export const toolHeaderActions = style({
  display: 'flex',
  alignItems: 'center',
  height: '1lh',
  gap: '2px',
  flexShrink: 0,
  opacity: 0,
  transition: 'opacity var(--transition)',
})

// Timestamp text in tool header actions (muted, small)
export const toolHeaderTimestamp = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
  userSelect: 'none',
  lineHeight: 1,
})

// The icon and the actions carry a box of one line and centre their content in
// it, so both take the top of the header rather than the baseline the text
// items share (see toolUseHeader). The actions area is right-aligned as well.
//
// Both rules are scoped to the header on purpose. `toolUseIcon` also sits in
// `thinkingHeader`, and `toolHeaderActions` also sits in the message rows (see
// the globalStyles on messageRow in ./messageStyles.css.ts), where a different
// container decides the alignment.
//
// A DESCENDANT selector, and a child combinator is wrong here. `<Tooltip>`
// wraps the icon in a `display: contents` span, which removes the wrapper's BOX
// but leaves the element in the DOM tree that a selector walks -- so the icon is
// a flex item of the header and NOT a DOM child of it. Under `>` this rule
// matches nothing, the icon keeps the header's baseline alignment, and the
// browser synthesizes its baseline at the bottom of its 1lh box: the title then
// sits 3px lower than the icon and the header grows to match.
globalStyle(`${toolUseHeader} .${toolUseIcon}`, {
  alignSelf: 'flex-start',
})

globalStyle(`${toolUseHeader} .${toolHeaderActions}`, {
  marginLeft: 'auto',
  alignSelf: 'flex-start',
})

// Body content indent for tool_use renderers. The transparent border-
// left reserves the slot so adding `toolBodyBorder` doesn't shift the
// content horizontally. Padding compensates for the 1px gap between
// `TOOL_BODY_INDENT + LINE_THICKNESS + space-3` (19) and the header's
// `iconSize.md + space-1` (20) so summary text aligns with title text.
export const toolBodyContent = style({
  marginLeft: `${TOOL_BODY_INDENT}px`,
  paddingLeft: 'calc(var(--space-3) + 1px)',
  paddingRight: 'var(--space-3)',
  borderLeft: `${LINE_THICKNESS}px solid transparent`,
})

// Recolors the slot reserved by `toolBodyContent`. Layered conditionally
// in `ToolUseLayout` so callers like the Task card can opt out via
// `bordered={false}`.
export const toolBodyBorder = style({
  borderLeftColor: 'var(--span-line-color, var(--border))',
})

// TodoList inside tool body: remove horizontal padding (toolBodyContent already provides it)
globalStyle(`${toolBodyContent} > .${todoList}`, {
  paddingLeft: 0,
  paddingRight: 0,
})

// File list in Grep/Glob tool results
export const toolFileList = style({
  paddingLeft: '20px',
  margin: '4px 0',
  fontSize: 'var(--text-8)',
})

// WebSearch result link list
export const webSearchLinkList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
})

export const webSearchLinkDomain = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-9)',
  flexShrink: 0,
  whiteSpace: 'nowrap',
})

// An image inside a tool result -- an MCP content block, a Read on a PNG, a
// screenshot. Constrained so a 4K screenshot doesn't take over the chat
// layout; the image keeps its aspect ratio and shrinks to fit.
// Exported so the renderer can reserve the exact final box (via inline
// aspect-ratio/width) for images whose intrinsic size it knows.
export const TOOL_IMAGE_MAX_HEIGHT_PX = 320
export const toolImage = style({
  display: 'block',
  maxWidth: '100%',
  maxHeight: `${TOOL_IMAGE_MAX_HEIGHT_PX}px`,
  width: 'auto',
  height: 'auto',
  objectFit: 'contain',
  borderRadius: 'var(--radius-medium)',
  border: '1px solid var(--border)',
  marginTop: 'var(--space-1)',
  marginBottom: 'var(--space-1)',
})

// The click target that opens a tool-result image in its own tab.
//
// `controlReset` because Oat's base layer paints every `button` as a solid
// primary pill; without the reset the image would sit inside a filled button
// with 8px/16px padding. The reset is LAYERED, so the block/cursor below
// survive it. `display: block` (not inline-flex) keeps the button's box the
// image's own box, which is what the intrinsic-size reservation assumes.
export const toolImageButton = style([controlReset, {
  display: 'block',
  cursor: 'zoom-in',
  borderRadius: 'var(--radius-medium)',
  selectors: {
    '&:focus-visible': {
      outline: '2px solid var(--primary)',
      outlineOffset: '2px',
    },
  },
}])

// Wrapper around a tool-result image, link or placeholder so it sits on its
// own line with the same spacing as toolInputSummary.
export const toolImageRow = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  marginTop: 'var(--space-1)',
  marginBottom: 'var(--space-1)',
})

// The SendMessage card's recipient, when it resolves to a subagent tab.
//
// `controlReset`, because Oat's base layer paints every `button` as a solid
// primary pill -- inline-flex, 8px/16px padding, a border and a radius -- and a
// class that sets only colour leaves all of it standing. Without the reset the
// recipient rendered as a filled button inside the one-line tool header, and the
// row changed height the moment the registry hydrated and turned the plain span
// into that button.
//
// That reset is LAYERED, so it cannot erase the clipping that `clippedText`
// declares, or the colours below. A bare `{ all: 'unset' }` in this list could:
// it ties with `clippedText` on specificity and wins whenever the bundler emits
// `~/styles/shared.css.ts` first, and the recipient then wraps instead of ending
// in an ellipsis.
export const toolRecipientLink = style([controlReset, clippedText, {
  cursor: 'pointer',
  color: 'var(--primary)',
  textDecoration: 'underline',
  textUnderlineOffset: '2px',
  selectors: {
    '&:hover': { color: 'rgb(from var(--primary) r g b / 0.8)' },
  },
}])

// A label/value list for a tool result's structured fields (the Agent card's
// agent id, model, output file). The label is muted and does not shrink, so the
// values line up while a long path is the part that wraps.
export const toolMetaList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  marginTop: 'var(--space-1)',
  marginBottom: 'var(--space-1)',
})

/**
 * One "label: value" line. Shared by the Agent result card's meta list and by
 * the WebSearch link rows, which are the same primitive: a baseline-aligned flex
 * row that clips. The role difference between the two lives entirely on the
 * CHILDREN -- WebSearch puts the muted non-shrinking part on the right (the
 * domain), the Agent card on the left (the label) -- so the row itself carries
 * no property either renderer owns.
 */
export const toolMetaRow = style({
  display: 'flex',
  alignItems: 'baseline',
  gap: 'var(--space-2)',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
  overflow: 'hidden',
})

export const toolMetaLabel = style({
  color: 'var(--muted-foreground)',
  flexShrink: 0,
  whiteSpace: 'nowrap',
})

// The value half. `minWidth: 0` is what lets a long path actually wrap inside
// the flex row instead of forcing the row wider than the card.
export const toolMetaValue = style({
  ...codeTypography,
  ...codeWrap,
  minWidth: 0,
})
