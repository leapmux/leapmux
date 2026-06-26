import type { WrapMetrics } from './chatWrapModel'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { rowCarriesDiff } from './chatHeightShared'
import { PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
import { diffWrappedRows, monoRowMetrics, proseRowMetrics, visualRows, wrapRowsForLine } from './chatWrapModel'
import { COLLAPSED_RESULT_ROWS } from './toolRenderers'

/**
 * Prefix that packs a tool name into a row's entry kind (`tool_use:${toolName}`),
 * so a tall Edit/Write diff is distinguishable from a short Read at estimate
 * time. Defined once here and shared by the packer (ChatView.entryKind) and the
 * two decoders (this module + chatHeightInput) so the literal can't drift.
 */
export const TOOL_USE_KIND_PREFIX = 'tool_use:'

/** The tool name packed into a `tool_use:${name}` entry kind, or undefined. */
export function toolNameFromKind(kind: string): string | undefined {
  return kind.startsWith(TOOL_USE_KIND_PREFIX) ? kind.slice(TOOL_USE_KIND_PREFIX.length) : undefined
}

/**
 * Dedicated, pure per-row height-estimation engine for the chat virtualizer.
 *
 * It computes the height a message row will occupy BEFORE it mounts, from how
 * that kind renders (CSS chrome + content metrics + interactive state), so the
 * virtualizer's offset map and scroll anchoring stay close to reality for rows
 * that haven't been measured yet. It replaces the old per-kind running mean.
 *
 * `estimateRowHeight` is a PURE function: deterministic, no DOM, no SolidJS, no
 * logging, no side effects. Given the same `(input, ctx)` it returns the same
 * number and the same breakdown — so it unit-tests cleanly. The impure parts
 * (reading the live content width, reading reactive UI state, and the
 * estimate-vs-actual WARN logging) live in the caller (`ChatView`).
 *
 * BIAS-UP RULE: when a line/wrap count is uncertain, round UP. A systematic
 * LOW bias is worse than a random one for scroll stability — estimate errors on
 * the off-screen rows ABOVE the scroll anchor SUM (they don't cancel like a
 * running mean's do), so a one-directional under-estimate grows cumulative
 * drift and re-trips the fling-suppression guard in useChatScroll. A slight
 * over-estimate instead self-corrects DOWNWARD when the row measures, which is
 * the benign direction. Every `ceil`/`max` below is deliberate.
 */

// ---------------------------------------------------------------------------
// Constants — seeded from CSS. Each cites its source so they can be re-verified
// when the styles change. The app sets no `html{font-size}`, so 1rem = 16px
// (browser default); message bubbles inherit that 16px base (they set
// line-height but not font-size, messageStyles.css.ts:4-11).
// ---------------------------------------------------------------------------

/**
 * Every calibration constant for the estimator, seeded from CSS and keyed by its
 * `HeightCtx` field name. `HeightCtx` and `defaultHeightCtx` are both DERIVED
 * from this object (`type HeightCtx = { contentWidthPx } & typeof HEIGHT_CONSTANTS`),
 * so adding a constant is a SINGLE edit here -- no separate interface field and
 * builder line to keep in sync. The only runtime-variable ctx field,
 * `contentWidthPx`, is supplied by `defaultHeightCtx`. Plain object (not
 * `as const`) so the derived field types stay `number`, leaving callers free to
 * pin a different value for a test or a calibration pass.
 */
const HEIGHT_CONSTANTS = {
  /** Prose line box: inherited 16px base x line-height 1.6 (messageStyles.css.ts:8). */
  proseLinePx: 25.6,
  /** Tool/meta header line box: --text-7 (14px) x 1.6 (toolStyles.css.ts:6-10). */
  toolLinePx: 22.4,
  /** Monospace result line box: --text-8 (12px) x 1.5 (toolStyles.css.ts:35-43). */
  monoLinePx: 18,
  /** Diff line box: --text-8 (12px) x 1.5 (diffStyles.css.ts:8-9). */
  diffLinePx: 18,
  /**
   * Shiki-highlighted JSON body / `toolInputSummary` label line box: 12px
   * (--text-8) x the INHERITED 1.6 line-height -> 19.2px. The shiki `<pre>` (a
   * RemoteTrigger JSON response) and the MCP "Arguments"/"Structured" labels set
   * no line-height override, so they inherit toolMessage's 1.6 (toolStyles.css.ts:6-10),
   * NOT the 1.5 of a plain `toolResultContentPre` (monoLinePx).
   */
  jsonLinePx: 19.2,
  /**
   * Fenced code-block line box: oat `code` is 0.875em (00-base.css:113-115), i.e.
   * 14px of the 16px bubble, x the inherited 1.6 line-height -> 22.4px. Code lines
   * render NON-wrapping (overflow-x scroll), so each source line is one such row.
   */
  codeBlockLinePx: 22.4,
  /** Proportional avg char advance ~= 0.5em at the 16px prose base. */
  proseAvgCharPx: 8,
  /** Monospace (Hack) avg char advance ~= 0.6em at the 12px mono size. */
  monoAvgCharPx: 7.2,
  /** messageBubble padding: --space-3 (12px) top + bottom (messageStyles.css.ts:6). */
  bubblePadV: 24,
  /** messageBubble padding: --space-4 (16px) left + right. */
  bubblePadH: 32,
  /** 1px solid/dashed border, top + bottom. */
  bubbleBorder: 2,
  /** messageBubble maxWidth: 85% of the row (messageStyles.css.ts:9). */
  bubbleMaxWidthFrac: 0.85,
  /** thinkingContent marginTop: --space-2 (messageStyles.css.ts:83-85). */
  thinkingContentMargin: 8,
  /**
   * Markdown block-gap margin between blank-line-separated blocks (paragraphs,
   * lists, headings): the --space-4 (16px) margin-bottom that oat's base CSS
   * gives <p>/<ul>/<pre> (@knadh/oat/css/00-base.css). A blank line in the source
   * renders as this gap, NOT a full text row.
   */
  blockGapPx: 16,
  /** attachmentList marginBottom --space-2 (messageStyles.css.ts:314); each item ~1 line. */
  attachmentListMargin: 8,
  /** Tool body left indent: TOOL_BODY_INDENT(5) + padding-left ~13 + padding-right 12. */
  toolBodyChromeH: 30,
  /** Collapsed tool-result cap: 3.6rem (toolStyles.css.ts:106-111). */
  collapsedCapPx: 57.6,
  /** Max rows shown in a collapsed tool result (toolRenderers.COLLAPSED_RESULT_ROWS). */
  collapsedResultRows: COLLAPSED_RESULT_ROWS,
  /** AskUserQuestion result: per-prompt marginBottom --space-1 (toolStyles.css.ts toolResultPrompt). */
  askPromptMarginPx: 4,
  /** WebSearch link-list inter-row gap: 2px (toolStyles.css.ts webSearchLinkList). */
  webSearchRowGapPx: 2,
  /** Diff container: 1px border x2 + marginTop --space-1 (diffStyles.css.ts:12-13). */
  diffContainerChrome: 6,
  /** Gap separator between hunks: padding --space-1 x2 + line + 1px dashed x2 (diffStyles.css.ts:114-127). */
  diffSeparatorPx: 26,
  /**
   * Diff-line gutter width subtracted from the container before wrapping the
   * content: two 4ch line-number columns + their --space-1 x2 margins + a 1.5ch
   * prefix + the line's --space-2 x2 padding (diffStyles.css.ts:36-71). At the
   * 12px mono size 1ch ~= 7.2px, so ~9.5ch + 24px ~= 92px.
   */
  diffGutterPx: 92,
  /**
   * Per-SIDE gutter for a split diff cell, subtracted from the half-width column
   * (the split grid is `1fr 1fr`, diffStyles.css.ts:94) before wrapping. Each cell
   * is a single 4ch line-number + 1.5ch prefix + --space-2 x2 padding
   * (DiffViewer's SplitDiffRow + diffStyles.css.ts:36-71): ~5.5ch + 16px ~= 56px.
   */
  diffSplitGutterPx: 56,
  /** MCP image cap: maxHeight 320px + margins (toolStyles.css.ts:294-305). */
  imageMaxPx: 320,
  imageMargins: 8,
  /** hiddenMessageJson: padding --space-2 (8) x2 + 1px border x2, maxHeight 300 (messageStyles.css.ts:140-154). */
  jsonPadV: 16,
  jsonBorder: 2,
  jsonMaxPx: 300,
  /**
   * TodoWrite / Plan-update list row (the alwaysVisible body under the tool
   * header): a --text-7 (14px) label at line-height 1.4 (~19.6px) beside a 20px
   * checkbox icon, with 3px x2 padding -> ~26px (TodoList.css.ts:14-23,42-51).
   */
  todoRowPx: 26,
  /** Inter-row gap in the todo list (TodoList.css.ts:3-8). */
  todoRowGap: 2,
  /** todoList vertical padding: --space-1 (4px) x2 (TodoList.css.ts:3-8). */
  todoListPadV: 8,
  /** ExitPlanMode / Plan body separator: the leading <hr> rule + its margins (MarkdownPlanLayout.tsx:45). */
  planHrPx: 17,
  /**
   * Calibrated body height for a Claude Task* card (TaskCreate/TaskUpdate/TaskGet
   * -> TaskCardMessage). The card's subject (header) and description (summary) both
   * resolve from the live todo store / paired tool_result, NOT this tool_use
   * message, so the estimator can't read them. WARN data shows these cards measure
   * ~49-68px = a 22.4px subject header plus a 1-2 line description summary; this
   * baseline sizes the below-header body so the total lands inside that band.
   */
  taskCardBodyPx: 40,
}

/**
 * A single contributing term in an estimate, surfaced in the WARN log so a
 * developer can see exactly WHICH part of the model was wrong.
 */
export interface EstimateTerm {
  label: string
  value: number
}

export interface EstimateBreakdown {
  /** The modelled kind, or `'fallback'` for an unmodelled kind. */
  kind: string
  /** Total estimated height in px (== the value the virtualizer uses). */
  total: number
  /** Ordered, summed contributions. */
  terms: EstimateTerm[]
  /** Raw inputs echoed for the WARN log (line counts, widths, state flags). */
  metrics: Record<string, number | string | boolean>
}

/**
 * Per-row descriptor: everything readable PRE-MOUNT for one row. Produced by
 * `buildHeightInput` from the already-parsed message + the interactive UI state
 * (which is keyed by message id and readable before the row mounts).
 */
export interface HeightInput {
  /** entryKind(): `category.kind` or `tool_use:${toolName}`. */
  kind: string
  toolName?: string
  hasSpanLines: boolean
  // --- content metrics (precomputed; no DOM) ---
  /** Char count of the prose/markdown/result body. */
  textLength?: number
  /** Explicit '\n' line count of the body. */
  logicalLineCount?: number
  /**
   * Per hard-line char counts of the prose/thinking body (blank lines = 0).
   * When present, the prose model sums each line's wrap count (and adds a
   * block-gap margin per interior blank line) instead of the coarser
   * `max(logicalLineCount, totalWrap)` — which under-counts markdown that mixes
   * short lines (bullets, bold "headers") with long wrapping paragraphs, since
   * the short lines don't pack into a long line's wasted row space.
   */
  lineLengths?: number[]
  /** Attachment chips above a user_content body. */
  attachmentCount?: number
  /** Child entries of a notification thread. */
  childCount?: number
  /** JSON line count for hidden/unsupported raw-JSON cards. */
  jsonLineCount?: number
  /** Number of MCP image attachments in a tool result. */
  imageCount?: number
  /**
   * tool_result body renders as 14px markdown (Agent/WebFetch/MCP) rather than
   * 12px monospace -- taller lines plus block-gap margins between paragraphs.
   */
  bodyMarkdown?: boolean
  /** TodoWrite checklist rows rendered in the alwaysVisible tool body. */
  todoCount?: number
  /** AskUserQuestion: number of questions (each gets a header row when >1). */
  askQuestionCount?: number
  /** AskUserQuestion: total option rows across all questions. */
  askOptionCount?: number
  /**
   * AskUserQuestion tool_RESULT: per-question `header: answer` row lengths, sized as
   * the compact AskUserQuestionResultView draws them (one prompt row each), not the
   * raw "Your questions have been answered: ..." content string.
   */
  askAnswerLineLengths?: number[]
  // --- custom tool_result renderer metrics (the provider hook supplies these for
  //     tools whose bespoke result view draws something other than the raw content
  //     string the generic text model sizes; see providers/claude/heightMetrics) ---
  /**
   * Leading `toolResultPrompt` summary line(s) the renderer draws ABOVE the body:
   * a 14px muted line + --space-1 marginBottom (toolStyles.css.ts:120-123), each
   * sized as toolLinePx + askPromptMarginPx. Grep/Glob ("N matches in M files"),
   * WebFetch ("200 OK -- 1.2 KB"), and an approved ExitPlanMode ("Plan file: ...")
   * each prepend one. NOT subject to the body collapse (it is an unmasked sibling).
   */
  summaryLineCount?: number
  /**
   * The renderer draws its FULL body with NO 3-row collapse clamp -- it bypasses
   * the shared collapse machinery (MCP's McpToolCallBody and ToolSearch's bare
   * <pre>). Exempts the body from the collapsedCapPx clamp, like an error result.
   */
  uncollapsed?: boolean
  /**
   * Overrides collapsedResultRows for the collapse gate AND the shown-row count.
   * Bash command output widens it to PROGRESS_MAX_ROWS when the body carried `\r`
   * progress overwrites (CommandResultBody's widened threshold).
   */
  collapsedRowThreshold?: number
  /**
   * tool_result body renders as shiki-highlighted JSON (RemoteTrigger's pretty
   * response): 12px mono wrapped, but at the 19.2px jsonLinePx box (shiki inherits
   * the 1.6 line-height) rather than the 18px monoLinePx of a plain <pre>.
   */
  jsonBody?: boolean
  /** MCP "Arguments" pre-block: line count of the pretty-printed tool input (always shown). */
  argsLineCount?: number
  /** MCP "Structured" pre-block: line count of the pretty-printed structuredContent. */
  structuredLineCount?: number
  /**
   * WebSearch result: number of link rows. Routes to estimateWebSearchResult,
   * which sizes the "N results" summary + min(N, collapse) never-wrapping 18px
   * link rows -- the structured card WebSearchResultsBody draws, NOT the raw
   * (decoupled / empty) content string the generic path would size.
   */
  webSearchLinkCount?: number
  // --- diff metrics (present iff the row renders a diff) ---
  diffUnifiedRows?: number
  diffSplitRows?: number
  diffHunkCount?: number
  diffAdded?: number
  diffRemoved?: number
  /** Per displayed hunk-line content length (prefix excluded) for unified wrap. */
  diffLineLengths?: number[]
  /**
   * Per split aligned-row content length (the LONGER of the two sides, prefix
   * excluded), for split-view wrap. One entry per rendered split row, mirroring
   * the renderer's pairing -- so its length is the true aligned-row count.
   */
  diffSplitLineLengths?: number[]
  /** Gap-separator rows: leading + between-hunk + trailing context gaps. */
  diffSeparatorRows?: number
  /**
   * Number of stacked diff containers the row renders (a multi-file edit draws
   * one block per file). Each block carries its own container chrome, so the
   * estimator charges chrome per block. Defaults to 1.
   */
  diffBlockCount?: number
  /**
   * Number of per-file label rows drawn ABOVE the diff blocks (file path + diff
   * stats badge). Codex draws one per file when a fileChange spans multiple
   * files; charged as one tool line each so a multi-file edit isn't under-sized.
   * Defaults to 0 (single-file edits and providers that draw no labels).
   */
  diffPerFileLabelRows?: number
  /**
   * A tool_USE row that renders a tool_result-style (collapsible) body rather than
   * a header + one-line summary: a Codex commandExecution's command output, an MCP
   * tool call's args/result, an ACP execute/read/search/fetch body. Routes the row
   * to the shared result-body model (collapse, status header, summary, mcp blocks)
   * instead of estimateToolUseHeader, so an off-screen estimate matches the body the
   * renderer actually draws. The body fields above (textLength, collapsed, bodyMarkdown,
   * argsLineCount, ...) are supplied by the provider hook exactly as for a tool_result.
   */
  toolUseRendersResultBody?: boolean
  /**
   * The result-body row ALSO draws a leading tool-title header line (it renders via
   * ToolUseLayout: MCP / collab / ACP). Omitted for a row whose settled view is a bare
   * result body with no tool title (Codex commandExecution renders ToolResultMessage).
   * Only read when `toolUseRendersResultBody` is set.
   */
  toolHeaderLine?: boolean
  // --- interactive state (read pre-mount, keyed by id) ---
  /** tool_result body NOT expanded -> clamp to the collapsed cap. */
  collapsed?: boolean
  /** tool_result reported is_error (kept for diagnostics in the WARN log). */
  isError?: boolean
  /** tool_result renders a leading ToolStatusHeader (error, or a sub-agent/task result). */
  hasHeader?: boolean
  /** thinking/plan/agent_prompt body shown. */
  expanded?: boolean
  diffView?: 'unified' | 'split'
}

/**
 * Environment shared by every row in one estimate batch (not per-item). The only
 * runtime-variable field is `contentWidthPx` (the measured inner width of the
 * message list); the rest are the CSS-derived `HEIGHT_CONSTANTS`, exposed so
 * tests can pin them and so the wrap model can be calibrated from the WARN
 * feedback loop. Derived from HEIGHT_CONSTANTS so the field list never drifts
 * from the values.
 */
export type HeightCtx = { contentWidthPx: number } & typeof HEIGHT_CONSTANTS

/**
 * Build a HeightCtx with the CSS-derived defaults; only the width varies at runtime.
 * A non-finite or non-positive width (a 0-width read from a hidden/unmounted pane, or
 * an arithmetic NaN) is clamped to PRE_MEASURE_WIDTH_PX: every estimator divides/
 * multiplies by contentWidthPx, so a NaN width would poison the row heights and, via
 * the cumulative offset map (where every comparison against NaN is false), silently
 * break the virtualizer's binary searches for every row past it. The sole live caller
 * already guards with `contentWidth() || PRE_MEASURE_WIDTH_PX`; enforcing it here makes
 * that invariant structural rather than a thing each caller must remember.
 */
export function defaultHeightCtx(contentWidthPx: number): HeightCtx {
  const safeWidth = Number.isFinite(contentWidthPx) && contentWidthPx > 0 ? contentWidthPx : PRE_MEASURE_WIDTH_PX
  return { contentWidthPx: safeWidth, ...HEIGHT_CONSTANTS }
}

/**
 * "Notable" estimate-vs-actual divergence worth a WARN: an absolute floor of
 * 24px combined (via MAX) with a relative floor, so small rows need a big
 * relative miss and tall rows a big absolute one. Catches STRUCTURAL
 * mis-estimates (wrong line count, missed collapse/expand state) while ignoring
 * sub-row wrap jitter.
 *
 * The relative floor is ASYMMETRIC, matching the bias-up rule's risk model:
 * - UNDER-estimates (estimate too SMALL) are dangerous -- the spacer above the
 *   anchor is too short, errors sum and grow cumulative scroll drift -- so they
 *   get the tighter 25% floor.
 * - OVER-estimates (estimate too LARGE) are the intended, SAFE direction -- each
 *   self-corrects DOWNWARD when the row measures -- so the deliberate per-line
 *   `ceil` wrap cushion (up to ~1 row per wrapping paragraph) is tolerated up to
 *   a looser 40% before it's worth a WARN. Big structural over-estimates (e.g. a
 *   961px guess for a 22px collapsed row) still clear 40% and surface.
 */
export const HEIGHT_WARN_ABS_PX = 24
export const HEIGHT_WARN_UNDER_PCT = 0.25
export const HEIGHT_WARN_OVER_PCT = 0.4
export function isHeightMismatchNotable(estimated: number, actual: number): boolean {
  const delta = actual - estimated
  const pct = delta >= 0 ? HEIGHT_WARN_UNDER_PCT : HEIGHT_WARN_OVER_PCT
  return Math.abs(delta) >= Math.max(HEIGHT_WARN_ABS_PX, actual * pct)
}

/** Minimal logger surface — kept structural so this file imports no logger module. */
interface WarnLogger { warn: (...args: unknown[]) => void }

/** Extra per-row context attached to the divergence WARN. */
export interface HeightMissLogContext {
  state: RowUiState
  /**
   * Full, untruncated raw message as a JSON value (the AgentChatMessage
   * envelope, `toJson`-encoded) so devtools renders it as an expandable
   * object rather than an escaped string. `unknown` keeps this file free of
   * the protobuf JsonValue dependency.
   */
  rawMessage: unknown
  /** The decoded structured payload as a JSON value (`parsed.topLevel`). */
  content: unknown
}

/**
 * Emit a WARN when a row's first measured height diverges notably from its
 * estimate, carrying the raw message + state + the estimate breakdown (every
 * contributing variable) vs the actual. Returns whether it warned. The caller
 * (ChatView) owns building `rawMessage`/`content` and wrapping this in a
 * try/catch so a bad payload can never crash a render.
 */
export function warnIfHeightMismatch(
  log: WarnLogger,
  id: string,
  breakdown: EstimateBreakdown,
  actual: number,
  context: HeightMissLogContext,
): boolean {
  if (!isHeightMismatchNotable(breakdown.total, actual))
    return false
  log.warn('chat row height estimate diverged', {
    id,
    kind: breakdown.kind,
    estimated: breakdown.total,
    actual,
    deltaPx: actual - breakdown.total,
    terms: breakdown.terms,
    metrics: breakdown.metrics,
    state: context.state,
    rawMessage: context.rawMessage,
    content: context.content,
  })
  return true
}

// The pure wrap-math engine (visualRows / wrapRowsForLine / proseRowsFromLines /
// proseRowMetrics / diffWrappedRows) lives in the chatWrapModel leaf -- see the
// imports at the top of this file.

/**
 * The leading "header line" term every tool/meta row starts with (the 14px tool
 * header). Centralized so a header-line calibration change lands in one place
 * rather than across the ~7 estimators that open with it.
 */
function toolHeaderTerm(ctx: HeightCtx): EstimateTerm {
  return { label: 'header line', value: ctx.toolLinePx }
}

/**
 * The message-bubble chrome term (vertical padding + top/bottom border) every
 * bubble-style row carries. Centralized so a bubble-chrome calibration change
 * lands in one place rather than across the estimators that open with it.
 */
function bubbleChromeTerm(ctx: HeightCtx): EstimateTerm {
  return { label: 'bubble pad+border', value: ctx.bubblePadV + ctx.bubbleBorder }
}

/** Sum a term list into a finished breakdown (rounds the total up — bias-up). */
function finish(kind: string, terms: EstimateTerm[], metrics: Record<string, number | string | boolean>): EstimateBreakdown {
  const total = Math.ceil(terms.reduce((s, t) => s + t.value, 0))
  return { kind, total, terms, metrics }
}

/**
 * Append a prose body's wrapped-row term and (when present) its block-gap term
 * to `terms`. Centralizes the rows-and-gaps pairing the prose/thinking/agent-
 * prompt/plan estimators share, so a row kind can't count wrapped rows but
 * forget the interior block-gap margins -- an under-estimate, the dangerous
 * (drift-causing) direction.
 */
function pushProseBody(terms: EstimateTerm[], metrics: WrapMetrics, ctx: HeightCtx, label: string): void {
  // `rows` is prose-wrapped rows PLUS code rows; split them so fenced code lines are
  // sized at the (shorter) code-block line box, not the prose line box.
  const proseRows = metrics.rows - metrics.codeRows
  if (proseRows > 0)
    terms.push({ label: `${label} ${proseRows} rows`, value: proseRows * ctx.proseLinePx })
  if (metrics.codeRows > 0)
    terms.push({ label: `${metrics.codeRows} code rows`, value: metrics.codeRows * ctx.codeBlockLinePx })
  if (metrics.gaps > 0)
    terms.push({ label: `${metrics.gaps} block gaps`, value: metrics.gaps * ctx.blockGapPx })
}

/**
 * Measure a conditionally-shown prose body at `width` and append its wrapped-row
 * and block-gap terms, returning the row/gap counts for the breakdown metadata.
 * Wraps the proseRowMetrics -> pushProseBody handoff the collapsible-thinking,
 * agent-prompt, and plan-body estimators share, so they can't drift on the pairing.
 */
function appendProseBody(terms: EstimateTerm[], input: HeightInput, ctx: HeightCtx, width: number, label: string): WrapMetrics {
  const metrics = proseRowMetrics(input, ctx.proseAvgCharPx, width)
  pushProseBody(terms, metrics, ctx, label)
  return metrics
}

/**
 * `appendProseBody` when `show`, else a no-op returning zero metrics. Collapses the
 * `let rows = 0; let gaps = 0; if (cond) { ... rows = m.rows; gaps = m.gaps }`
 * plumbing the collapsible-thinking, agent-prompt, and plan-body estimators each
 * repeated, so none can add the rows term to `finish`'s breakdown but forget the
 * matching `blockGaps` (an under-count, the drift direction the estimator avoids).
 */
function appendOptionalProseBody(terms: EstimateTerm[], show: boolean, input: HeightInput, ctx: HeightCtx, width: number, label: string): WrapMetrics {
  return show ? appendProseBody(terms, input, ctx, width, label) : { rows: 0, gaps: 0, codeRows: 0 }
}

const PROSE_KINDS = new Set([
  'assistant_text',
  'user_text',
  'user_content',
  'compact_summary',
  'unknown',
])

// Claude Task* cards (single-row TaskCardMessage): header subject + summary.
const TASK_CARD_KINDS = new Set([
  'tool_use:TaskCreate',
  'tool_use:TaskUpdate',
  'tool_use:TaskGet',
])

/**
 * Estimate the rendered height (px, EXCLUDING the inter-row gap, matching
 * `heightOfIndex`) of a single chat row. Pure.
 */
export function estimateRowHeight(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const kind = input.kind
  const toolName = input.toolName ?? toolNameFromKind(kind)

  // A row that carries a diff (tool_use:Edit/Write or a tool_result whose
  // content parsed into hunks) renders the diff block regardless of which side.
  if (rowCarriesDiff(input))
    return estimateDiffRow(input, ctx, toolName)

  if (kind === 'tool_result')
    return estimateToolResult(input, ctx)

  // TodoWrite, ExitPlanMode, and AskUserQuestion render an alwaysVisible body
  // under the tool header (a checklist / a markdown plan / a question+options
  // list), not just a header line.
  if (kind === 'tool_use:TodoWrite')
    return estimateTodoList(input, ctx)
  if (kind === 'tool_use:ExitPlanMode')
    return estimatePlanBody(input, ctx)
  if (kind === 'tool_use:AskUserQuestion')
    return estimateAskUserQuestion(input, ctx)
  if (TASK_CARD_KINDS.has(kind))
    return estimateTaskCard(input, ctx)

  if (kind.startsWith(TOOL_USE_KIND_PREFIX)) {
    // A tool_use row that renders a tool_result-style collapsible body (Codex
    // commandExecution / MCP / collab, ACP execute/read/search/fetch) is sized by the
    // shared result-body model, optionally prefixed by its tool-title header line;
    // everything else (Bash command summary, etc.) is a header + one-line summary.
    if (input.toolUseRendersResultBody)
      return estimateToolUseBody(input, ctx)
    return estimateToolUseHeader(input, ctx, toolName)
  }

  if (kind === 'assistant_thinking' || kind === 'plan_execution')
    return estimateCollapsibleProse(input, ctx)

  if (kind === 'agent_prompt')
    return estimateAgentPrompt(input, ctx)

  if (kind === 'hidden' || kind === 'unsupported_provider')
    return estimateJsonCard(input, ctx)

  if (kind === 'result_divider' || kind === 'control_response')
    return estimateSingleLineMeta(input, ctx)

  if (kind === 'notification')
    return estimateNotification(input, ctx)

  // task_notification renders as a ToolUseLayout (header + optional summary),
  // NOT a centered notification bubble -- size it like a header-only tool row.
  if (kind === 'task_notification')
    return estimateToolUseHeader(input, ctx, toolName)

  if (PROSE_KINDS.has(kind))
    return estimateProse(input, ctx)

  // Unmodelled kind: a conservative single prose bubble, flagged so the WARN
  // log surfaces it as a `fallback` (a signal to model it).
  return finish('fallback', [
    bubbleChromeTerm(ctx),
    { label: 'one line', value: ctx.proseLinePx },
  ], { kind })
}

/** Usable text width inside an 85%-max-width prose bubble. */
function proseTextWidth(ctx: HeightCtx): number {
  return Math.max(1, ctx.contentWidthPx * ctx.bubbleMaxWidthFrac - ctx.bubblePadH)
}

/** Usable text width inside a stretched tool/meta row (indent + padding subtracted). */
function toolTextWidth(ctx: HeightCtx): number {
  return Math.max(1, ctx.contentWidthPx - ctx.toolBodyChromeH)
}

/** Usable wrap width for a UNIFIED diff line (the full content column minus the gutter). */
function diffUnifiedWidth(ctx: HeightCtx): number {
  return ctx.contentWidthPx - ctx.diffGutterPx
}

/**
 * Usable wrap width for one SIDE of a SPLIT diff (the half-width `1fr 1fr` column
 * minus the per-cell gutter). Can go NEGATIVE on a degenerately narrow pane;
 * wrapRowsForLine floors the divisor at a few chars, so the negative is safe to
 * pass through rather than guard here.
 */
function diffSplitWidth(ctx: HeightCtx): number {
  return ctx.contentWidthPx / 2 - ctx.diffSplitGutterPx
}

function estimateProse(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const textLength = input.textLength ?? 0
  const logical = input.logicalLineCount ?? 1
  const width = proseTextWidth(ctx)
  const metrics = proseRowMetrics(input, ctx.proseAvgCharPx, width)
  const attachments = input.attachmentCount ?? 0
  const terms: EstimateTerm[] = [
    bubbleChromeTerm(ctx),
  ]
  pushProseBody(terms, metrics, ctx, 'text')
  if (attachments > 0)
    terms.push({ label: `${attachments} attachments`, value: attachments * ctx.proseLinePx + ctx.attachmentListMargin })
  return finish(input.kind, terms, {
    textLength,
    logicalLineCount: logical,
    visualRows: metrics.rows,
    codeRows: metrics.codeRows,
    blockGaps: metrics.gaps,
    contentWidthPx: width,
    attachments,
  })
}

function estimateCollapsibleProse(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  // Thinking defaults expanded; plan_execution defaults collapsed. The resolved
  // flag is supplied by the caller (it reads the per-id UI state + global pref).
  // The thinkingHeader/planExecution header sets no font-size override, so it
  // inherits the bubble's 16px base (proseLinePx), NOT the 14px tool header.
  const terms: EstimateTerm[] = [
    bubbleChromeTerm(ctx),
    { label: 'header line', value: ctx.proseLinePx },
  ]
  const textLength = input.textLength ?? 0
  const logical = input.logicalLineCount ?? 1
  if (input.expanded)
    terms.push({ label: 'content marginTop', value: ctx.thinkingContentMargin })
  const { rows, gaps } = appendOptionalProseBody(terms, !!input.expanded, input, ctx, proseTextWidth(ctx), 'text')
  return finish(input.kind, terms, {
    expanded: !!input.expanded,
    textLength,
    logicalLineCount: logical,
    visualRows: rows,
    blockGaps: gaps,
  })
}

/**
 * agent_prompt (a sub-agent task prompt) renders via ToolUseLayout: a tool
 * header ("Prompt") whose markdown body is shown ONLY when expanded. It defaults
 * COLLAPSED (MESSAGE_UI_KEY.AGENT_PROMPT), so the common case is a bare header
 * line -- NOT a prose bubble (the body is a tool-indented document, no bubble
 * chrome). When expanded it adds the full prompt body.
 */
function estimateAgentPrompt(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const terms: EstimateTerm[] = [toolHeaderTerm(ctx)]
  const { rows, gaps } = appendOptionalProseBody(terms, !!input.expanded, input, ctx, toolTextWidth(ctx), 'body')
  return finish(input.kind, terms, {
    expanded: !!input.expanded,
    textLength: input.textLength ?? 0,
    visualRows: rows,
    blockGaps: gaps,
  })
}

/**
 * TodoWrite (and Plan-update) renders an alwaysVisible checklist body under the
 * tool header: one row per todo plus the list's vertical padding. An empty list
 * renders the "cleared" placeholder, modeled as the bare header.
 */
function estimateTodoList(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const todos = input.todoCount ?? 0
  const terms: EstimateTerm[] = [toolHeaderTerm(ctx)]
  if (todos > 0) {
    const body = ctx.todoListPadV + todos * ctx.todoRowPx + Math.max(0, todos - 1) * ctx.todoRowGap
    terms.push({ label: `${todos} todo rows`, value: body })
  }
  return finish(input.kind, terms, { todoCount: todos })
}

/**
 * ExitPlanMode renders the plan as an alwaysVisible markdown body (MarkdownPlan-
 * Layout) under the tool header: a header line, a leading <hr> separator, and
 * the plan prose wrapped at the tool body width (16px markdown, like a sub-agent
 * prompt body). Empty plan text suppresses the body.
 */
function estimatePlanBody(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const terms: EstimateTerm[] = [toolHeaderTerm(ctx)]
  const show = (input.textLength ?? 0) > 0
  if (show)
    terms.push({ label: 'plan separator', value: ctx.planHrPx })
  const { rows, gaps } = appendOptionalProseBody(terms, show, input, ctx, toolTextWidth(ctx), 'plan body')
  return finish(input.kind, terms, {
    textLength: input.textLength ?? 0,
    visualRows: rows,
    blockGaps: gaps,
  })
}

function estimateToolUseHeader(input: HeightInput, ctx: HeightCtx, toolName?: string): EstimateBreakdown {
  // Header-only tools (Read/Grep/Glob/WebFetch/Bash-collapsed): one header line
  // plus any summary lines rendered under it. The summary renders in a `pre-wrap`
  // monospace block (toolInputSummary), so a multi-line command (an EXPANDED Bash
  // tool_use) wraps EACH hard line independently. Size it with monoRowMetrics, which
  // SUMS per-line wraps from `lineLengths` -- the flat visualRows max() ignores the
  // end-of-line slack on every wrapped line and under-counts a several-long-line
  // command, the off-screen-too-short direction that jumps the row up on mount.
  // monoRowMetrics falls back to the same flat visualRows when no per-line lengths
  // were fed (the single-line Read/Grep/Glob summaries), so this is a no-op there.
  const width = toolTextWidth(ctx)
  const textLength = input.textLength ?? 0
  const logical = input.logicalLineCount ?? 0
  const summaryRows = textLength > 0 || logical > 0
    ? monoRowMetrics(input, ctx.monoAvgCharPx, width)
    : 0
  const terms: EstimateTerm[] = [toolHeaderTerm(ctx)]
  // Each summary row is charged at jsonLinePx (the 1.6 line box toolInputSummary
  // INHERITS from toolMessage at the 12px text-8 size), NOT monoLinePx (the 1.5 box
  // of a plain toolResultContentPre). The 12px char advance (monoAvgCharPx) used for
  // the wrap count above is correct -- only the per-row height was the wrong box,
  // matching pushMcpExtraBlocks which already charges its toolInputSummary label at
  // jsonLinePx.
  if (summaryRows > 0)
    terms.push({ label: `summary ${summaryRows} rows`, value: summaryRows * ctx.jsonLinePx })
  return finish(input.kind, terms, { toolName: toolName ?? '', summaryRows, textLength })
}

/**
 * A tool_USE row that renders a tool_result-style body (Codex commandExecution / MCP
 * / collab, ACP execute/read/search/fetch). Sizes the body with the SAME collapse /
 * status-header / summary / mcp-block model as a tool_result (estimateToolResult),
 * then prefixes the tool-title header line when the row renders via ToolUseLayout
 * (`toolHeaderLine`). A Codex commandExecution renders ToolResultMessage instead, so
 * it has no tool title -- only the result body (with its own error status header).
 */
function estimateToolUseBody(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const body = estimateToolResult(input, ctx)
  if (!input.toolHeaderLine)
    return body
  // Re-sum the raw term values (not the body's already-ceiled total) with the tool
  // header prepended, so the header line is counted once.
  return finish(body.kind, [toolHeaderTerm(ctx), ...body.terms], { ...body.metrics, toolHeaderLine: 1 })
}

/**
 * A collapsed tool_result clamps its body -- images included -- to collapsedCapPx
 * behind `overflow: hidden` (TOOL_RESULT_EXPANDED defaults false, so collapsed is
 * the DEFAULT). Without honoring that, a tall result is estimated at full height
 * while it renders a ~58px sliver: a large over-estimate that inflates the spacer
 * above the anchor until the row mounts and measures. Shared by both result models.
 */
function collapsedBodyPx(px: number, ctx: HeightCtx): number {
  return Math.min(px, ctx.collapsedCapPx)
}

/**
 * A tool_result's height has two unrelated models behind one classification:
 * image-bearing results (sized by image chrome) and text/markdown/mono results
 * (sized by wrapped rows). Dispatched by image presence; each model is a focused
 * function so a change to one collapse/sizing rule doesn't have to step around the
 * other's interleaved branches.
 */
function estimateToolResult(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  if (input.askAnswerLineLengths)
    return estimateAskAnswers(input, ctx)
  // WebSearch draws a fixed-structure link card (summary + never-wrapping link
  // rows), unrelated to the raw content string -- size it from the link count.
  if (input.webSearchLinkCount != null)
    return estimateWebSearchResult(input, ctx)
  return (input.imageCount ?? 0) > 0
    ? estimateImageResult(input, ctx)
    : estimateTextResult(input, ctx)
}

/**
 * The MCP renderer (McpToolCallBody) draws, below its content body, an always-
 * visible "Arguments" block (label + pretty-printed tool input) and optional
 * "Structured" block (label + pretty structuredContent) -- neither of which is in
 * the tool_result content string the body model sizes, and neither collapses. Each
 * present block adds one label line (jsonLinePx, the toolInputSummary inherits the
 * 1.6 line-height) + N mono body lines. Shared by the text and image result models.
 */
function pushMcpExtraBlocks(terms: EstimateTerm[], input: HeightInput, ctx: HeightCtx): void {
  const args = input.argsLineCount ?? 0
  if (args > 0) {
    terms.push({ label: 'mcp args label', value: ctx.jsonLinePx })
    terms.push({ label: `mcp args ${args} rows`, value: args * ctx.monoLinePx })
  }
  const structured = input.structuredLineCount ?? 0
  if (structured > 0) {
    terms.push({ label: 'mcp structured label', value: ctx.jsonLinePx })
    terms.push({ label: `mcp structured ${structured} rows`, value: structured * ctx.monoLinePx })
  }
}

/**
 * WebSearch tool_RESULT (WebSearchResultsBody): a "N results" summary prompt line
 * plus one never-wrapping 18px link row per result (white-space: nowrap + ellipsis,
 * toolStyles.css.ts webSearchLink) with a 2px inter-row gap. The link list collapses
 * by ITEM count (useCollapsedItems) -- min(N, collapsedResultRows) rows, the whole
 * list clipped to collapsedCapPx -- while the summary sits OUTSIDE the fade. When
 * expanded, the markdown summary below the list is sized too (its metrics are
 * supplied as the body text). Sized from the link count, NOT the (empty) content.
 */
function estimateWebSearchResult(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const count = input.webSearchLinkCount ?? 0
  const collapsed = !!input.collapsed
  const terms: EstimateTerm[] = [
    { label: 'results summary', value: ctx.toolLinePx + ctx.askPromptMarginPx },
  ]
  const collapseList = collapsed && count > ctx.collapsedResultRows
  const shown = collapseList ? ctx.collapsedResultRows : count
  if (count > 0) {
    let listPx = shown * ctx.monoLinePx + Math.max(0, shown - 1) * ctx.webSearchRowGapPx
    if (collapseList)
      listPx = collapsedBodyPx(listPx, ctx)
    terms.push({ label: `${shown} link rows`, value: listPx })
  }
  // Expanded (not collapsed) also reveals the markdown summary below the list.
  if (!collapsed && (input.textLength ?? 0) > 0)
    pushProseBody(terms, proseRowMetrics(input, ctx.proseAvgCharPx, toolTextWidth(ctx)), ctx, 'summary')
  return finish(input.kind, terms, { webSearchLinkCount: count, shownLinks: shown, collapsed })
}

/**
 * AskUserQuestion tool_RESULT: one `header: answer` prompt row per question
 * (AskUserQuestionResultView). Each row is 14px proportional text (toolLinePx) that
 * wraps when long, plus a --space-1 marginBottom. Sized from the per-question row
 * lengths the Claude hook extracts, NOT the raw result content (which is the long
 * "Your questions have been answered: ..." string the generic path would over-size).
 */
function estimateAskAnswers(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const width = toolTextWidth(ctx)
  const lens = input.askAnswerLineLengths ?? []
  let rows = 0
  for (const len of lens)
    rows += wrapRowsForLine(len, ctx.proseAvgCharPx, width)
  const terms: EstimateTerm[] = [
    { label: `${lens.length} answer prompts (${rows} rows)`, value: rows * ctx.toolLinePx },
  ]
  if (lens.length > 0)
    terms.push({ label: `${lens.length} prompt margins`, value: lens.length * ctx.askPromptMarginPx })
  return finish(input.kind, terms, { askAnswerCount: lens.length, rows })
}

function estimateImageResult(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const width = toolTextWidth(ctx)
  const textLength = input.textLength ?? 0
  const logical = input.logicalLineCount ?? 1
  // Images dominate when present (capped at 320px each), but an MCP result can
  // carry a text body alongside the image(s) -- count it too so a "here is the
  // page: <screenshot>" result isn't under-estimated by its prose height.
  const n = input.imageCount! // guarded by estimateToolResult's dispatch
  const imagesPx = n * (ctx.imageMaxPx + ctx.imageMargins)
  // When the body is markdown (MCP/Agent results render 14px markdown, bodyMarkdown),
  // size it with the SAME prose model estimateTextResult uses -- proseAvgCharPx wraps
  // at toolLinePx plus inter-block gaps. The 12px monoAvgCharPx/monoLinePx under-counts
  // the wider markdown glyphs and biases the off-screen estimate DOWN (the drift-
  // accumulating direction). Monospace bodies keep the mono flat-row model.
  let bodyPx = 0
  if (textLength > 0) {
    if (input.bodyMarkdown) {
      const { rows, gaps } = proseRowMetrics(input, ctx.proseAvgCharPx, width)
      bodyPx = rows * ctx.toolLinePx + gaps * ctx.blockGapPx
    }
    else {
      bodyPx = visualRows(textLength, logical, ctx.monoAvgCharPx, width) * ctx.monoLinePx
    }
  }
  const terms: EstimateTerm[] = []
  // Only the renderers that actually draw a ToolStatusHeader charge one. The MCP
  // image renderer (McpToolCallBody) draws none -- charging it here was a phantom
  // header. hasHeader is the universal "leading status header present" signal.
  if (input.hasHeader)
    terms.push(toolHeaderTerm(ctx))
  // MCP results bypass the collapse machinery (uncollapsed): the full image(s) +
  // body render. Other image results clamp to the collapsedCapPx sliver.
  if (input.collapsed && !input.uncollapsed) {
    terms.push({ label: 'collapsed body (images)', value: collapsedBodyPx(imagesPx + bodyPx, ctx) })
  }
  else {
    terms.push({ label: `${n} images`, value: imagesPx })
    if (bodyPx > 0)
      terms.push({ label: 'body', value: bodyPx })
  }
  pushMcpExtraBlocks(terms, input, ctx)
  return finish(input.kind, terms, { imageCount: n, textLength, collapsed: !!input.collapsed && !input.uncollapsed })
}

function estimateTextResult(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const width = toolTextWidth(ctx)
  const textLength = input.textLength ?? 0
  const logical = input.logicalLineCount ?? 1
  // Agent/WebFetch/MCP results render their body as 14px markdown (toolLinePx
  // line + block-gap margins between paragraphs); Read/Grep/command/pre results
  // render 12px monospace (monoLinePx, no gaps). The markdown body is
  // PROPORTIONAL text, so wrap it with proseAvgCharPx (like every other prose
  // model) -- the 12px monoAvgCharPx would under-count wraps for the wider 14px
  // glyphs, biasing the off-screen estimate DOWN (the drift-accumulating
  // direction). Monospace bodies keep monoAvgCharPx.
  const markdown = !!input.bodyMarkdown
  // An error result renders its FULL body (no collapse) as inherited 14px text
  // (toolLinePx), NOT the 12px mono / 3-row collapsed body the success path uses --
  // whether via the catch-all toolResultError div (ToolResultMessage bypasses the
  // collapse for isError) or a custom error view (ExitPlanMode's MarkdownText
  // feedback). So errors size like markdown for line height and never collapse.
  const isError = !!input.isError
  // RemoteTrigger renders a shiki-highlighted JSON body (jsonBody) whose <pre>
  // inherits the 1.6 line-height (jsonLinePx 19.2), not the 1.5 of a plain mono pre.
  const jsonBody = !!input.jsonBody
  const lineHeight = jsonBody ? ctx.jsonLinePx : (markdown || isError ? ctx.toolLinePx : ctx.monoLinePx)
  // Mono/JSON bodies render `white-space: pre-wrap`, so each hard line wraps on its
  // own: monoRowMetrics SUMS per-line wraps rather than the flat visualRows max(),
  // which under-counts a body of several long lines (the divergence this fixes).
  // An empty body (e.g. an approved ExitPlanMode that draws only a header + "Plan
  // file:" prompt) contributes no rows -- skip the per-line floor's phantom row.
  const hasBody = textLength > 0
  const { rows: fullRows, gaps } = !hasBody
    ? { rows: 0, gaps: 0 }
    : markdown
      ? proseRowMetrics(input, ctx.proseAvgCharPx, width)
      : { rows: monoRowMetrics(input, ctx.monoAvgCharPx, width), gaps: 0 }
  // A renderer that draws its full body (MCP / ToolSearch) never collapses; the
  // collapse threshold widens past 3 for `\r`-progress Bash output.
  const uncollapsed = !!input.uncollapsed
  const threshold = input.collapsedRowThreshold ?? ctx.collapsedResultRows
  // Most successful tool_results render body-only: Read/Grep/Glob bypass the
  // status header, and a successful command result skips it. The header is
  // present only when the result errored OR it's a sub-agent/task result, whose
  // bodies always wrap in a ToolStatusHeader ("Agent ... completed", etc.).
  const terms: EstimateTerm[] = []
  if (input.hasHeader)
    terms.push({ label: 'status header line', value: ctx.toolLinePx })
  // Leading `toolResultPrompt` summary line(s) (Grep/Glob/WebFetch/ExitPlanMode):
  // an unmasked sibling above the body, so charged outside the collapse clamp.
  const summaryLines = input.summaryLineCount ?? 0
  if (summaryLines > 0)
    terms.push({ label: `${summaryLines} summary line(s)`, value: summaryLines * (ctx.toolLinePx + ctx.askPromptMarginPx) })
  if (input.collapsed && !isError && !uncollapsed && logical > threshold) {
    // Collapsed: at most `threshold` rows visible, hard-clamped to the cap.
    // Gated on logical > threshold to MIRROR the renderer, which only collapses
    // when there are MORE than that many LOGICAL lines (useCollapsedLines'
    // hasMoreLinesThan / useCollapsedItems' items.length > threshold). A body with
    // <= that many lines renders fully even when its long lines WRAP past the cap --
    // capping it here was the under-estimate. An error result and the full-body
    // renderers (uncollapsed) are exempt (they render their full body, see above).
    const shownRows = Math.min(fullRows, threshold)
    terms.push({ label: `collapsed body (<=${threshold} rows)`, value: collapsedBodyPx(shownRows * lineHeight, ctx) })
  }
  else if (fullRows > 0) {
    terms.push({ label: `body ${fullRows} rows`, value: fullRows * lineHeight })
    if (gaps > 0)
      terms.push({ label: `${gaps} block gaps`, value: gaps * ctx.blockGapPx })
  }
  // MCP results draw always-visible Arguments / Structured pre-blocks below the body.
  pushMcpExtraBlocks(terms, input, ctx)
  return finish(input.kind, terms, {
    collapsed: !!input.collapsed,
    isError: !!input.isError,
    hasHeader: !!input.hasHeader,
    markdown,
    jsonBody,
    uncollapsed,
    summaryLines,
    threshold,
    textLength,
    logicalLineCount: logical,
    fullRows,
  })
}

// Per-line wrap model when the line lengths were extracted, else the flat
// aligned-row count -- the wrap-vs-flat rule the split and unified branches of
// estimateDiffRow both apply, so it can't drift between them.
function wrapOrFlat(lengths: number[] | undefined, charWidthPx: number, widthPx: number, flat: number): number {
  return lengths && lengths.length > 0
    ? diffWrappedRows(lengths, charWidthPx, widthPx)
    : flat
}

function estimateDiffRow(input: HeightInput, ctx: HeightCtx, toolName?: string): EstimateBreakdown {
  const split = input.diffView === 'split'
  // Both views wrap per line (diffLine is pre-wrap/break-all); they differ only
  // in the wrap width -- split lays the two sides out in half-width `1fr 1fr`
  // columns, so each cell wraps far sooner -- and in the flat fallback's row-count
  // preference (its own view first). Prefer the per-line wrap model; fall back to
  // the flat aligned-row count when per-line lengths weren't extracted.
  const rows = split
    ? wrapOrFlat(input.diffSplitLineLengths, ctx.monoAvgCharPx, diffSplitWidth(ctx), input.diffSplitRows ?? input.diffUnifiedRows ?? 0)
    : wrapOrFlat(input.diffLineLengths, ctx.monoAvgCharPx, diffUnifiedWidth(ctx), input.diffUnifiedRows ?? input.diffSplitRows ?? 0)
  const hunks = input.diffHunkCount ?? 1
  // Gap separators (leading/between/trailing context gaps) render when the diff
  // carries `originalFile`; otherwise only the between-hunk synthetic separators.
  const separators = input.diffSeparatorRows ?? Math.max(0, hunks - 1)
  // A multi-file edit stacks one diff container per file, each with its own
  // chrome (border + margin); charge it per block so an N-file edit isn't sized
  // as a single block (which would under-count by (N-1) chromes -> drift).
  const blocks = Math.max(1, input.diffBlockCount ?? 1)
  // Per-file label rows (path + diff-stats badge) drawn above the blocks when a
  // multi-file edit labels each file; one tool line each.
  const perFileLabels = Math.max(0, input.diffPerFileLabelRows ?? 0)
  // A headerless diff: a Claude/Pi file-edit tool_RESULT (FileEditDiffBody is
  // body-only), OR a tool_USE row that renders a tool_result-style body -- a Codex
  // completed `fileChange` mounts via ToolResultMessage / a bare toolMessage div with
  // NO tool-title header, so its height fields carry `toolUseRendersResultBody`. An ACP
  // tool_USE edit, by contrast, renders inside ToolUseLayout (a real tool header) and
  // leaves the flag unset, so it still charges the header here.
  const terms: EstimateTerm[] = []
  if (input.kind !== 'tool_result' && !input.toolUseRendersResultBody)
    terms.push(toolHeaderTerm(ctx))
  terms.push({ label: `diff container chrome${blocks > 1 ? ` x${blocks}` : ''}`, value: ctx.diffContainerChrome * blocks })
  if (perFileLabels > 0)
    terms.push({ label: `${perFileLabels} per-file labels`, value: perFileLabels * ctx.toolLinePx })
  terms.push({ label: `${rows} diff rows (${split ? 'split' : 'unified'})`, value: rows * ctx.diffLinePx })
  if (separators > 0)
    terms.push({ label: `${separators} gap separators`, value: separators * ctx.diffSeparatorPx })
  return finish(input.kind, terms, {
    toolName: toolName ?? '',
    diffView: split ? 'split' : 'unified',
    rows,
    hunkCount: hunks,
    separators,
    added: input.diffAdded ?? 0,
    removed: input.diffRemoved ?? 0,
  })
}

function estimateJsonCard(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  // Floor at one body line: an empty rawText yields jsonLineCount 0 (not nullish),
  // which would otherwise collapse the card to bare chrome.
  const lines = Math.max(1, input.jsonLineCount ?? input.logicalLineCount ?? 1)
  const body = Math.min(lines * ctx.monoLinePx, ctx.jsonMaxPx)
  const terms: EstimateTerm[] = [
    { label: 'json pad+border', value: ctx.jsonPadV + ctx.jsonBorder },
    { label: `json body (cap ${ctx.jsonMaxPx})`, value: body },
  ]
  if (input.kind === 'unsupported_provider')
    terms.push({ label: 'danger line', value: ctx.toolLinePx })
  return finish(input.kind, terms, { jsonLineCount: lines })
}

function estimateSingleLineMeta(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  // result_divider / control_response: a single flex line. An error result_divider
  // carries a `<pre>` detail below it -- 14px monospace (resultErrorDetail), pre-wrap
  // + word-break, so a long single line WRAPS. Size its rows from the WRAPPED count
  // (textLength against the tool body width, floored at the logical \n count) at the
  // 14px line (toolLinePx), not the 12px monoLinePx. Wrap with proseAvgCharPx -- the
  // 14px choice the prose model makes, since the 12px monoAvgCharPx under-counts a 14px
  // glyph and biases DOWN, the offset-map-drifting direction this file avoids. Charging
  // only logicalLineCount sized a long single-line error to one row (an under-estimate).
  const logical = input.logicalLineCount ?? 0
  // resultErrorDetail is `pre-wrap` -- each hard line (each errors[] entry) wraps
  // independently, so SUM per-line wraps (proseRowMetrics) rather than the flat
  // visualRows max(), which ignores end-of-line slack and under-counts several long
  // error lines (the same defect the tool-summary fix addressed). proseRowMetrics
  // falls back to the flat model when no lineLengths were fed (e.g. control_response),
  // so that path is unchanged.
  const rows = logical > 0
    ? proseRowMetrics(input, ctx.proseAvgCharPx, toolTextWidth(ctx)).rows
    : 0
  const terms: EstimateTerm[] = [{ label: 'line', value: ctx.toolLinePx }]
  if (rows > 0)
    terms.push({ label: `error detail ${rows} rows`, value: rows * ctx.toolLinePx })
  return finish(input.kind, terms, { extraLines: logical, errorDetailRows: rows })
}

function estimateNotification(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  // Nested thread. The systemMessage bubble renders PROPORTIONAL text at --text-7
  // (14px), so charge the 14px line height (toolLinePx) and wrap with proseAvgCharPx
  // -- the same choice the prose-body model makes: the 12px monoAvgCharPx is NARROWER
  // than a 14px proportional glyph, so it under-counts wraps and biases the estimate
  // DOWN, the offset-map-drifting direction this file works to avoid.
  const children = Math.max(1, input.childCount ?? 1)
  const textLength = input.textLength ?? 0
  // Size from the COALESCED body the renderer emits, NOT one line per child:
  // buildHeightInput fills textLength + logicalLineCount from notificationThreadMetrics
  // (the renderer's own coalescing), where logicalLineCount is the laid-out BLOCK count
  // -- consecutive children collapse into one comma-joined paragraph, many children
  // drop to a few wrapped lines. visualRows floors the wrapped rows at that block count
  // and rounds up, so the estimate stays biased up (the WARN log catches misses)
  // without the ~8x child-count inflation a coalesced thread previously took.
  const logical = Math.max(1, input.logicalLineCount ?? 1)
  const width = proseTextWidth(ctx)
  const rows = visualRows(textLength, logical, ctx.proseAvgCharPx, width)
  return finish(input.kind, [
    bubbleChromeTerm(ctx),
    { label: `${rows} rows across ${children} children`, value: rows * ctx.toolLinePx },
  ], { childCount: children, visualRows: rows })
}

/**
 * A Claude Task* card (TaskCreate/TaskUpdate/TaskGet -> TaskCardMessage): a header
 * carrying the task subject plus a description summary below it. Both the subject
 * and description resolve from the live todo store / paired tool_result -- not
 * this tool_use message (a status-only update is just `{taskId, status}`) -- so
 * the body can't be read from here and is sized to a calibrated baseline.
 */
function estimateTaskCard(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  // Record the baseline + any readable inputs so the estimate-vs-actual WARN has
  // something to calibrate against -- the body is sized to a guess (the live
  // subject/description aren't on this tool_use), so a divergence log with no
  // metrics is un-actionable for exactly the row kind that's hardest to size.
  return finish(input.kind, [
    { label: 'subject header line', value: ctx.toolLinePx },
    { label: 'task card body (baseline)', value: ctx.taskCardBodyPx },
  ], { taskCardBodyPx: ctx.taskCardBodyPx, textLength: input.textLength ?? 0 })
}

/**
 * AskUserQuestion renders an alwaysVisible body under the header: for each
 * question a bold header row (only when there is more than one question) plus one
 * 12px summary row per option (toolInputSummary). See askUserQuestion.tsx.
 */
function estimateAskUserQuestion(input: HeightInput, ctx: HeightCtx): EstimateBreakdown {
  const questions = input.askQuestionCount ?? 0
  const options = input.askOptionCount ?? 0
  const terms: EstimateTerm[] = [toolHeaderTerm(ctx)]
  if (questions > 1)
    terms.push({ label: `${questions} question headers`, value: questions * ctx.toolLinePx })
  if (options > 0)
    terms.push({ label: `${options} option rows`, value: options * ctx.monoLinePx })
  return finish(input.kind, terms, { askQuestionCount: questions, askOptionCount: options })
}

// ---------------------------------------------------------------------------
// Estimate-cache keys. The estimator's output is memoized per row by a global
// EPOCH (inputs that shift EVERY row's height -> clears the whole cache) and a
// per-row KEY (inputs that shift ONE row's height -> re-estimates that row).
// Co-located with estimateRowHeight and built through these typed helpers so the
// dependency set is an explicit named type, single-homed: a new height input is
// added to one interface + builder rather than being silently omitted from an
// ad-hoc pipe-joined string at the call site.
// ---------------------------------------------------------------------------

/**
 * Per-row inputs the analytical height depends on BEYOND the global epoch. A change
 * to any field re-estimates the row (its cached estimate is stale). Live streaming
 * text is DELIBERATELY excluded (see ChatView's estimateKey wiring): a streaming
 * body grows every delta and lives outside msg.content, so folding it in would
 * re-run the estimator per chunk; streaming rows measure at the tail instead.
 */
export interface EstimateKeyInputs {
  /** Message seq -- a reseq / in-place consolidation bumps it. */
  seq: bigint
  /** A paired tool_use sibling is available (a tool_result sizes from its opener). */
  hasToolUseSibling: boolean
  /**
   * The paired tool_use OPENER's content version (0 for non-result rows / no opener).
   * A tool_result sizes its diff from the opener's input, and the opener is a
   * DIFFERENT message: an in-place same-seq opener edit bumps the OPENER's version
   * while the result's own seq and contentVersion stay put, so without this the
   * off-screen result's cached estimate would survive the change.
   */
  toolUseContentVersion: number
  /** Per-message UI version -- a per-row expand / diff-view toggle bumps it. */
  uiVersion: number
  /** Content version -- a same-seq in-place body replacement (keeping the seq) bumps it. */
  contentVersion: number
  /**
   * Whether a renderable command stream is present for the row. Some classifiers
   * size a row from stream PRESENCE (e.g. an empty-body Codex reasoning item
   * classifies `assistant_thinking` vs `hidden` on it), so the estimate output can
   * change when presence flips. It flips at most twice per row lifecycle (first
   * content / clear), not per-delta, so folding it in is cheap -- unlike the
   * streaming TEXT, which is deliberately excluded (see ChatView's virtualItems).
   */
  hasCommandStream: boolean
}

export function buildEstimateKey(inputs: EstimateKeyInputs): string {
  return `${inputs.seq}|${inputs.hasToolUseSibling ? 's' : ''}|${inputs.toolUseContentVersion}|${inputs.uiVersion}|${inputs.contentVersion}|${inputs.hasCommandStream ? 'c' : ''}`
}

/**
 * Global inputs that shift EVERY row's analytical height, so a change clears the
 * whole estimate cache (vs the per-row key above).
 */
export interface EstimateEpochInputs {
  /** Measured content width -- the wrap width every analytical estimate depends on. */
  contentWidth: number
  /** "Expand agent thoughts" pref -- changes every thinking row's height. */
  expandAgentThoughts: boolean
  /** Diff-view mode (unified / split) -- changes every diff row's height. */
  diffView: DiffViewPreference
}

export function buildEstimateEpoch(inputs: EstimateEpochInputs): string {
  return `${inputs.contentWidth}|${inputs.expandAgentThoughts ? 1 : 0}|${inputs.diffView}`
}

// ---------------------------------------------------------------------------
// Estimator input types. The feature-EXTRACTION that builds a HeightInput from a
// parsed message (the impure-ish, provider-sniffing half) lives in a separate
// module, `chatHeightInput.ts`, so this file stays a pure function; the two
// share only the `HeightInput` / `RowUiState` types declared here.
// ---------------------------------------------------------------------------

/** The interactive, per-message UI state the estimator needs, resolved pre-mount. */
export interface RowUiState {
  collapsed: boolean
  expanded: boolean
  /** tool-use-layout body expanded (e.g. a multi-line Bash command shown in full). */
  toolBodyExpanded: boolean
  diffView: 'unified' | 'split'
}
