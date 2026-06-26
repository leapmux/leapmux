import type { HeightInput } from './chatHeightEstimator'
import type { MessageCategory } from './messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import {
  buildEstimateEpoch,
  buildEstimateKey,
  defaultHeightCtx,
  estimateRowHeight,
  isHeightMismatchNotable,
  warnIfHeightMismatch,
} from './chatHeightEstimator'
import { buildHeightInput as buildHeightInputRaw } from './chatHeightInput'
import { PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
// Register the provider plugins so `buildHeightInput`'s heightMetrics dispatch
// resolves a real plugin (the Claude hook below supplies diff / bodyMarkdown /
// hasHeader / result_divider detail from the Claude-shaped wire data).
import './providers'

const CTX = defaultHeightCtx(800)

// The feature-extraction tests below feed Claude-shaped wire data through the
// orchestrator + Claude `heightMetrics` hook, so default the provider. Explicit
// `agentProvider` in args still wins.
function buildHeightInput(args: Parameters<typeof buildHeightInputRaw>[0]): HeightInput {
  return buildHeightInputRaw({ agentProvider: AgentProvider.CLAUDE_CODE, ...args })
}

function est(input: HeightInput, contentWidthPx = 800): number {
  return estimateRowHeight(input, defaultHeightCtx(contentWidthPx)).total
}

describe('chatheightestimator defaultHeightCtx width clamp', () => {
  it('passes a valid positive width through unchanged', () => {
    expect(defaultHeightCtx(900).contentWidthPx).toBe(900)
  })

  it('clamps a non-finite or non-positive width to PRE_MEASURE_WIDTH_PX', () => {
    // A 0-width read from a hidden/unmounted pane, or a NaN/Infinity from arithmetic,
    // would poison every estimator AND the cumulative offset map (comparisons against
    // NaN are all false), so the width is clamped to the pre-measure default.
    expect(defaultHeightCtx(Number.NaN).contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
    expect(defaultHeightCtx(0).contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
    expect(defaultHeightCtx(-10).contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
    expect(defaultHeightCtx(Number.POSITIVE_INFINITY).contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
  })

  it('keeps a row estimate finite when the width is non-finite (no offset-map poison)', () => {
    const input: HeightInput = { kind: 'assistant_text', hasSpanLines: false, textLength: 200, logicalLineCount: 4 }
    const fromNaN = est(input, Number.NaN)
    expect(Number.isFinite(fromNaN)).toBe(true)
    // The NaN width clamps to PRE_MEASURE_WIDTH_PX, so the estimate matches that width.
    expect(fromNaN).toBe(est(input, PRE_MEASURE_WIDTH_PX))
  })
})

describe('chatheightestimator estimateRowHeight', () => {
  it('estimates a header-only tool as a small single-line band', () => {
    const header = est({ kind: 'tool_use:Read', hasSpanLines: false })
    const prose = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 5, logicalLineCount: 1 })
    // A header-only tool is just one tool line; far shorter than a prose bubble
    // (which adds bubble padding + border around its text).
    expect(header).toBeLessThan(prose)
    expect(header).toBeGreaterThan(0)
    expect(header).toBeLessThan(30)
  })

  it('clamps a collapsed tool result to the collapse cap regardless of line count', () => {
    // Both have MORE than COLLAPSED_RESULT_ROWS (3) lines, so both actually collapse.
    const small = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, logicalLineCount: 10, textLength: 60 })
    const huge = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, logicalLineCount: 500, textLength: 30000 })
    // Collapsed shows at most COLLAPSED_RESULT_ROWS rows clamped to ~57.6px, so 3
    // lines and 500 lines estimate identically.
    expect(small).toBe(huge)
    // header (22.4) + collapsed cap (57.6) -> 80.
    expect(huge).toBeLessThanOrEqual(80)
  })

  it('does NOT collapse an error tool_result -- it renders the full body (toolResultError)', () => {
    const ok = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, logicalLineCount: 19, textLength: 230 })
    const error = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, isError: true, hasHeader: true, logicalLineCount: 19, textLength: 230 })
    // The success result clamps to the 3-row collapse cap; the error result renders
    // all 19 lines (the renderer's isError branch bypasses the collapse), so it is
    // far taller -- the under-estimate this fixes.
    expect(error).toBeGreaterThan(ok * 4)
    // Full body at the 14px error line box (toolLinePx 22.4) plus the status header.
    expect(error).toBeGreaterThan(19 * 22)
  })

  it('does NOT collapse a tool result with <= COLLAPSED_RESULT_ROWS lines, even when they wrap', () => {
    // 3 long lines that each wrap to ~2 visual rows. The renderer only collapses when
    // there are MORE than 3 LOGICAL lines, so this renders fully (~6 rows), not capped
    // at the 3-row (54px) collapse cap -- the under-estimate this fixes.
    const wrapping = est({
      kind: 'tool_result',
      hasSpanLines: false,
      collapsed: true,
      lineLengths: [200, 200, 200],
      logicalLineCount: 3,
      textLength: 600,
    }, 800)
    // Per-line wrapping of 3 long mono lines is well above the 54px collapse cap.
    expect(wrapping).toBeGreaterThan(90)
  })

  it('sums per-line wraps for a mono tool body, not the flat total (pre-wrap)', () => {
    // 4 lines that each just cross a row boundary (~110 chars at this width), so each
    // wraps to 2 rows -> 8 visual rows. The flat char-total model wastes no slack and
    // gives only ~5, under-counting; the renderer wraps each line independently.
    const perLine = est({
      kind: 'tool_result',
      hasSpanLines: false,
      lineLengths: [110, 110, 110, 110],
      logicalLineCount: 4,
      textLength: 440,
    }, 800)
    const flat = est({ kind: 'tool_result', hasSpanLines: false, logicalLineCount: 4, textLength: 440 }, 800)
    expect(perLine).toBeGreaterThan(flat)
  })

  it('sums per-line wraps for a multi-line tool_use command summary, not the flat total (pre-wrap)', () => {
    // An EXPANDED multi-line Bash command renders under the header in a pre-wrap mono
    // block (toolInputSummary), wrapping each hard line independently. 4 lines that
    // each just cross a row boundary (~110 chars at this width) wrap to ~8 visual
    // rows; the flat char-total model wastes no end-of-line slack and under-counts at
    // ~5. estimateToolUseHeader must sum per-line via lineLengths (the off-screen
    // estimate was too short, jumping the row up when it mounts and re-measures).
    const perLine = est({
      kind: 'tool_use:Bash',
      hasSpanLines: false,
      lineLengths: [110, 110, 110, 110],
      logicalLineCount: 4,
      textLength: 440,
    }, 800)
    const flat = est({ kind: 'tool_use:Bash', hasSpanLines: false, logicalLineCount: 4, textLength: 440 }, 800)
    expect(perLine).toBeGreaterThan(flat)
  })

  it('charges the tool_use summary at the inherited 1.6 line box (jsonLinePx), not the 1.5 monoLinePx', () => {
    // toolInputSummary is 12px text-8 with NO line-height, so it inherits toolMessage's
    // 1.6 box (19.2px / jsonLinePx), NOT the 1.5 box (18px / monoLinePx) of a plain
    // toolResultContentPre. 10 single-char lines each take exactly one wrap row.
    const ctx = defaultHeightCtx(800)
    const b = estimateRowHeight({ kind: 'tool_use:Bash', hasSpanLines: false, lineLengths: Array.from<number>({ length: 10 }).fill(1), logicalLineCount: 10, textLength: 19 }, ctx)
    const summaryTerm = b.terms.find(t => t.label.startsWith('summary'))!
    expect(summaryTerm.value).toBeCloseTo(10 * ctx.jsonLinePx, 5) // 192, not 180
  })

  it('sums per-line wraps for a multi-line result_divider error detail (pre-wrap), not the flat total', () => {
    // resultErrorDetail is pre-wrap: 4 errors, each ~110 chars at this width wraps to 2
    // rows -> ~8 rows. The flat char-total model wastes no end-of-line slack -> ~5,
    // under-counting the off-screen row (the same defect the tool-summary fix addressed).
    const perLine = est({ kind: 'result_divider', hasSpanLines: false, lineLengths: [110, 110, 110, 110], logicalLineCount: 4, textLength: 440 }, 800)
    const flat = est({ kind: 'result_divider', hasSpanLines: false, logicalLineCount: 4, textLength: 440 }, 800)
    expect(perLine).toBeGreaterThan(flat)
  })

  it('sizes an AskUserQuestion result by its answer prompts, not the raw answered-content text', () => {
    // The raw result content ("Your questions have been answered: <full question>...")
    // is hundreds of chars; AskUserQuestionResultView shows one compact "header:
    // answer" row per question. One short prompt -> ~1 row (~27px), not 3 wrapped
    // mono rows (54px) the generic text path would give.
    const ask = est({
      kind: 'tool_result',
      hasSpanLines: false,
      collapsed: true,
      askAnswerLineLengths: [45],
      logicalLineCount: 1,
      textLength: 352,
    }, 875)
    expect(ask).toBeGreaterThan(20)
    expect(ask).toBeLessThan(40) // not the 54px mono-text path
  })

  it('sizes a fenced code block as non-wrapping rows, not char-wrapped prose', () => {
    // 50 code lines of 200 chars each (encoded negative by toMarkdownLineLengths),
    // plus a one-line intro. As prose each 200-char line wraps to several rows; as
    // code each is exactly ONE row -- the 7x over-estimate this fixes.
    const code = Array.from<number>({ length: 50 }).fill(-201)
    const withCode = est({ kind: 'user_content', hasSpanLines: false, lineLengths: [10, ...code], logicalLineCount: 51, textLength: 50 * 200 + 10 })
    // Char-wrapping 50x200-char lines would be several thousand px; code-aware it is
    // ~51 source rows (1 prose + 50 code) + chrome.
    expect(withCode).toBeLessThan(51 * 26 + 200)
    // And the 50 code rows are sized at the code line box (~22.4 each).
    expect(withCode).toBeGreaterThan(50 * 21)
  })

  it('grows an expanded tool result monotonically with line count', () => {
    const ten = est({ kind: 'tool_result', hasSpanLines: false, logicalLineCount: 10, textLength: 50 })
    const hundred = est({ kind: 'tool_result', hasSpanLines: false, logicalLineCount: 100, textLength: 500 })
    const thousand = est({ kind: 'tool_result', hasSpanLines: false, logicalLineCount: 1000, textLength: 5000 })
    expect(hundred).toBeGreaterThan(ten)
    expect(thousand).toBeGreaterThan(hundred)
  })

  it('estimates a split diff shorter than unified for a mixed-change hunk', () => {
    const base = { kind: 'tool_result', hasSpanLines: false, diffUnifiedRows: 8, diffSplitRows: 5, diffHunkCount: 1, diffAdded: 3, diffRemoved: 3 } as const
    const unified = est({ ...base, diffView: 'unified' })
    const split = est({ ...base, diffView: 'split' })
    // Split aligns +/- rows side by side (fewer grid rows than unified stacking).
    expect(split).toBeLessThan(unified)
    // Both scale with row count.
    const taller = est({ ...base, diffView: 'unified', diffUnifiedRows: 80 })
    expect(taller).toBeGreaterThan(unified)
  })

  it('floors the wrap width on a degenerately narrow pane so prose cannot explode into phantom rows', () => {
    // textLength 400 at a 20px content pane. A 1px wrap-width floor would blow this
    // up to ~3200 rows (400*8/1) and tens of thousands of px; the shared
    // charWidthPx*4 floor caps it at ceil(400/4) = 100 rows, a bounded over-estimate.
    const narrow = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 400, logicalLineCount: 1 }, 20)
    expect(narrow).toBeGreaterThan(0)
    expect(narrow).toBeLessThan(3000)
    // A normal-width pane estimates the same single line as far shorter.
    const wide = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 400, logicalLineCount: 1 }, 800)
    expect(wide).toBeLessThan(narrow)
  })

  it('adds a per-separator term for multi-hunk diffs', () => {
    const one = est({ kind: 'tool_use:Edit', hasSpanLines: false, diffUnifiedRows: 10, diffHunkCount: 1, diffView: 'unified' })
    const three = est({ kind: 'tool_use:Edit', hasSpanLines: false, diffUnifiedRows: 10, diffHunkCount: 3, diffView: 'unified' })
    expect(three).toBeGreaterThan(one) // 2 hunk separators added
  })

  it('omits the tool-title header for a tool_use diff that renders a result-style body (Codex fileChange)', () => {
    // A Codex completed `fileChange` is a tool_USE kind but mounts HEADERLESS
    // (ToolResultMessage / bare toolMessage div). It flags toolUseRendersResultBody so
    // estimateDiffRow drops the header it would otherwise charge for a non-tool_result
    // kind. An ACP tool_USE edit (real ToolUseLayout header) leaves the flag unset and
    // keeps the header -- so the two differ by exactly one toolHeaderTerm (22.4px).
    const base = { kind: 'tool_use:fileChange', hasSpanLines: false, diffUnifiedRows: 4, diffHunkCount: 1, diffView: 'unified' } as const
    const withHeader = estimateRowHeight(base, CTX) // ACP-edit shape: header charged
    const headerless = estimateRowHeight({ ...base, toolUseRendersResultBody: true }, CTX)
    expect(withHeader.terms.some(t => t.label === 'header line')).toBe(true)
    expect(headerless.terms.some(t => t.label === 'header line')).toBe(false)
    // The totals are Math.ceil'd, so allow ±1px of rounding around the 22.4px header.
    expect(Math.abs((withHeader.total - headerless.total) - 22.4)).toBeLessThanOrEqual(1)
  })

  it('charges container chrome per diff block (a multi-file edit stacks N containers)', () => {
    const base = { kind: 'tool_result', hasSpanLines: false, diffUnifiedRows: 4, diffHunkCount: 1, diffView: 'unified' } as const
    const oneBlock = est(base)
    const twoBlocks = est({ ...base, diffBlockCount: 2 })
    // The second stacked block adds exactly one diffContainerChrome (6px); a
    // single concatenated block would under-count by that amount.
    expect(twoBlocks - oneBlock).toBe(6)
  })

  it('charges one tool line per per-file label row above the diff blocks', () => {
    const base = { kind: 'tool_result', hasSpanLines: false, diffUnifiedRows: 4, diffHunkCount: 1, diffBlockCount: 2, diffView: 'unified' } as const
    const noLabels = est(base)
    const withLabels = est({ ...base, diffPerFileLabelRows: 2 })
    // Two per-file labels (path + stats badge) add two 14px tool lines (44.8px);
    // the total is Math.ceil'd, so allow ±1px of rounding.
    expect(Math.abs((withLabels - noLabels) - 2 * 22.4)).toBeLessThanOrEqual(1)
  })

  it('wraps a markdown tool_result body with the proportional advance, not mono', () => {
    // toolTextWidth(800) = 770. A 100-char line wraps to 2 rows at the proportional
    // 8px advance (100*8/770 = 1.04 -> 2) but would be only 1 row at the 12px-mono
    // 7.2px advance (100*7.2/770 = 0.94 -> 1). Markdown bodies are proportional, so
    // the wider advance (the safe bias-up direction) must be used.
    const oneRow = est({ kind: 'tool_result', hasSpanLines: false, bodyMarkdown: true, lineLengths: [1], logicalLineCount: 1, textLength: 1 })
    const twoRows = est({ kind: 'tool_result', hasSpanLines: false, bodyMarkdown: true, lineLengths: [100], logicalLineCount: 1, textLength: 100 })
    // Exactly one extra 14px tool line (22.4px, Math.ceil'd to 22-23) -- at the
    // narrower mono advance the 100-char line would still be one row, delta 0.
    expect(twoRows - oneRow).toBeGreaterThanOrEqual(22)
    expect(twoRows - oneRow).toBeLessThanOrEqual(23)
  })

  it('wraps long split-view lines at the half-width column (taller than unified)', () => {
    // A single long changed line. Split lays it out in a half-width `1fr`
    // column, so it wraps to MORE visual rows than the full-width unified view.
    const base: HeightInput = {
      kind: 'tool_result',
      hasSpanLines: false,
      diffHunkCount: 1,
      diffUnifiedRows: 1,
      diffSplitRows: 1,
      diffLineLengths: [200],
      diffSplitLineLengths: [200],
    }
    const unified = est({ ...base, diffView: 'unified' })
    const split = est({ ...base, diffView: 'split' })
    expect(split).toBeGreaterThan(unified)
  })

  it('does not over-wrap short split-view lines', () => {
    // A short line fits in the half-width column, so per-line wrap yields the
    // same single aligned row as the flat fallback count.
    const wrapped = est({ kind: 'tool_result', hasSpanLines: false, diffHunkCount: 1, diffSplitRows: 1, diffSplitLineLengths: [8], diffView: 'split' })
    const flat = est({ kind: 'tool_result', hasSpanLines: false, diffHunkCount: 1, diffSplitRows: 1, diffView: 'split' })
    expect(wrapped).toBe(flat)
  })

  it('floors the diff wrap width so a pathologically narrow pane does not explode the estimate', () => {
    const base: HeightInput = {
      kind: 'tool_result',
      hasSpanLines: false,
      diffHunkCount: 1,
      diffSplitRows: 1,
      diffSplitLineLengths: [200],
      diffView: 'split',
    }
    // contentWidthPx 50 drives the split half-column width (contentWidthPx/2 minus
    // the gutter) negative. A 1px divisor would wrap a 200-char line into ~1400
    // phantom rows (tens of thousands of px); the char-width floor keeps it sane.
    const narrow = est(base, 50)
    expect(narrow).toBeGreaterThan(0)
    expect(narrow).toBeLessThan(2000)
  })

  it('estimates single-line prose as one line plus bubble chrome', () => {
    const h = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 10, logicalLineCount: 1 })
    // bubble pad+border (26) + one prose line (25.6) -> 52.
    expect(h).toBe(52)
  })

  it('estimates narrower prose as taller (wrap is width-dependent)', () => {
    const input: HeightInput = { kind: 'assistant_text', hasSpanLines: false, textLength: 2000, logicalLineCount: 1 }
    const narrow = est(input, 300)
    const wide = est(input, 900)
    expect(narrow).toBeGreaterThan(wide)
  })

  it('estimates expanded thinking taller than collapsed', () => {
    const base = { kind: 'assistant_thinking', hasSpanLines: false, textLength: 500, logicalLineCount: 5 } as const
    expect(est({ ...base, expanded: true })).toBeGreaterThan(est({ ...base, expanded: false }))
  })

  it('returns a positive floor for empty content (never 0 or NaN)', () => {
    const h = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 0, logicalLineCount: 0 })
    expect(h).toBeGreaterThan(0)
    expect(Number.isFinite(h)).toBe(true)
  })

  it('bounds a hidden raw-JSON card by the 300px cap', () => {
    const small = est({ kind: 'hidden', hasSpanLines: false, jsonLineCount: 4 })
    const huge = est({ kind: 'hidden', hasSpanLines: false, jsonLineCount: 5000 })
    expect(small).toBeGreaterThan(0)
    expect(huge).toBeLessThanOrEqual(320) // pad+border (18) + cap (300)
  })

  it('bounds an unsupported_provider card by the JSON cap plus a danger line', () => {
    const h = est({ kind: 'unsupported_provider', hasSpanLines: false, jsonLineCount: 5000 })
    expect(h).toBeLessThanOrEqual(345)
    expect(h).toBeGreaterThan(300)
  })

  it('floors an empty raw-JSON card at one body line (jsonLineCount 0)', () => {
    // countLines('') is 0, which is not nullish -- without the Math.max(1, ...)
    // floor the card would collapse to bare chrome.
    const empty = est({ kind: 'hidden', hasSpanLines: false, jsonLineCount: 0 })
    const oneLine = est({ kind: 'hidden', hasSpanLines: false, jsonLineCount: 1 })
    expect(empty).toBe(oneLine)
    expect(empty).toBeGreaterThan(18) // pad+border (18) + at least one mono line
  })

  it('bounds an MCP image tool result by the 320px-per-image cap', () => {
    const one = est({ kind: 'tool_result', hasSpanLines: false, imageCount: 1 })
    const two = est({ kind: 'tool_result', hasSpanLines: false, imageCount: 2 })
    expect(one).toBeGreaterThan(320)
    expect(one).toBeLessThan(360)
    expect(two).toBeGreaterThan(one)
  })

  it('clamps a COLLAPSED image tool result to the collapse cap, not full per-image height', () => {
    // Collapsed is the default for tool_results; the renderer hides the overflow
    // behind a ~58px cap, so a multi-image collapsed result must NOT be sized at
    // ~320px/image -- a large over-estimate that inflates the spacer above the
    // anchor until the row mounts and measures.
    const collapsed = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, imageCount: 3 })
    const expanded = est({ kind: 'tool_result', hasSpanLines: false, imageCount: 3 })
    expect(collapsed).toBeLessThan(100) // collapse cap (~57.6); no header (none drawn)
    expect(expanded).toBeGreaterThan(900) // 3 * ~320px
    expect(collapsed).toBeLessThan(expanded)
  })

  it('sizes an MCP image result\'s markdown caption with the prose model, not mono', () => {
    // estimateImageResult must honor bodyMarkdown: an MCP image result can carry a
    // markdown caption ("here is the page: <screenshot>"), which wraps with the
    // wider proseAvgCharPx at the taller toolLinePx plus inter-block gaps. Sizing it
    // as 12px mono under-counts the caption -- the drift-accumulating direction.
    const base: HeightInput = {
      kind: 'tool_result',
      hasSpanLines: false,
      uncollapsed: true,
      imageCount: 1,
      lineLengths: [200, 0, 200],
      logicalLineCount: 3,
      textLength: 401,
    }
    const markdown = est({ ...base, bodyMarkdown: true })
    const mono = est({ ...base, bodyMarkdown: false })
    expect(markdown).toBeGreaterThan(mono)
    // The caption genuinely adds height beyond the lone image.
    expect(markdown).toBeGreaterThan(est({ kind: 'tool_result', hasSpanLines: false, uncollapsed: true, imageCount: 1 }))
  })

  it('flags an unmodelled kind as a fallback', () => {
    const r = estimateRowHeight({ kind: 'totally_unknown_kind', hasSpanLines: false }, CTX)
    expect(r.kind).toBe('fallback')
    expect(r.total).toBeGreaterThan(0)
  })

  it('is deterministic (same input -> identical output)', () => {
    const input: HeightInput = { kind: 'assistant_text', hasSpanLines: false, textLength: 1234, logicalLineCount: 7 }
    const a = estimateRowHeight(input, CTX)
    const b = estimateRowHeight(input, CTX)
    expect(a).toEqual(b)
  })

  it('never decreases the estimate as content grows (monotonic)', () => {
    let prev = 0
    for (const textLength of [0, 100, 1000, 5000, 20000]) {
      const h = est({ kind: 'assistant_text', hasSpanLines: false, textLength, logicalLineCount: 1 })
      expect(h).toBeGreaterThanOrEqual(prev)
      prev = h
    }
  })

  it('rounds a partial wrapped line UP to a full line (bias-up)', () => {
    // ~1.5 lines of chars at width 800 must estimate >= the 2-line height, never
    // truncate down to 1 line.
    const oneLine = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 10, logicalLineCount: 1 })
    const onePointFive = est({ kind: 'assistant_text', hasSpanLines: false, textLength: 130, logicalLineCount: 1 })
    expect(onePointFive).toBeGreaterThan(oneLine)
  })

  it('returns a structured breakdown enumerating contributing terms', () => {
    const r = estimateRowHeight({ kind: 'assistant_text', hasSpanLines: false, textLength: 400, logicalLineCount: 3 }, CTX)
    expect(r.terms.length).toBeGreaterThanOrEqual(2)
    expect(r.terms.reduce((s, t) => s + t.value, 0)).toBeCloseTo(r.total, 0)
    expect(r.metrics.visualRows).toBeGreaterThan(0)
  })

  it('estimates single-line meta rows (divider, control response) as one line', () => {
    const divider = est({ kind: 'result_divider', hasSpanLines: false })
    const control = est({ kind: 'control_response', hasSpanLines: false })
    expect(divider).toBeLessThan(30)
    expect(control).toBeLessThan(30)
    // A result_divider with an error detail below it grows.
    const withDetail = est({ kind: 'result_divider', hasSpanLines: false, logicalLineCount: 4 })
    expect(withDetail).toBeGreaterThan(divider)
  })

  it('estimates a collapsed agent_prompt as a bare header line, not the full prompt', () => {
    // agent_prompt defaults collapsed: its body only renders when expanded, so a
    // long prompt collapses to just the "Prompt" header (~one tool line).
    const longPrompt = { kind: 'agent_prompt', hasSpanLines: false, textLength: 3000, logicalLineCount: 30, lineLengths: Array.from<number>({ length: 30 }).fill(100) } as const
    const collapsed = est({ ...longPrompt, expanded: false })
    const expanded = est({ ...longPrompt, expanded: true })
    expect(collapsed).toBeLessThan(30) // header only (~22.4px), NOT the 30-row body
    expect(collapsed).toBeGreaterThan(0)
    // Expanding renders the full prompt body, so it is far taller.
    expect(expanded).toBeGreaterThan(collapsed + 500)
  })

  it('estimates notification as positive prose-like rows', () => {
    const note = est({ kind: 'notification', hasSpanLines: false, childCount: 2, textLength: 60, logicalLineCount: 2 })
    expect(note).toBeGreaterThan(0)
  })

  it('sizes a notification by its coalesced body (block count + wrap), not the child count', () => {
    // Many children that coalesce to the SAME rendered body (same block count + text)
    // estimate the SAME height -- the child count no longer inflates a coalesced
    // thread ~8x (buildHeightInput fills logicalLineCount from the renderer's block
    // count, so 20 comma-joined children become a couple of wrapped lines, not 20).
    const few = est({ kind: 'notification', hasSpanLines: false, childCount: 2, textLength: 30, logicalLineCount: 2 })
    const many = est({ kind: 'notification', hasSpanLines: false, childCount: 20, textLength: 30, logicalLineCount: 2 })
    expect(many).toBe(few)
    // A genuinely taller body (more rendered blocks) IS taller -- the estimate still
    // tracks the coalesced line count and biases up.
    const taller = est({ kind: 'notification', hasSpanLines: false, childCount: 2, textLength: 30, logicalLineCount: 5 })
    expect(taller).toBeGreaterThan(few)
  })

  it('adds height for user_content attachments', () => {
    const base = { kind: 'user_content', hasSpanLines: false, textLength: 20, logicalLineCount: 1 } as const
    expect(est({ ...base, attachmentCount: 3 })).toBeGreaterThan(est({ ...base, attachmentCount: 0 }))
  })

  it('does not mutate its frozen inputs', () => {
    const input = Object.freeze<HeightInput>({ kind: 'assistant_text', hasSpanLines: false, textLength: 100, logicalLineCount: 2 })
    const ctx = Object.freeze(defaultHeightCtx(800))
    expect(() => estimateRowHeight(input, ctx)).not.toThrow()
  })
})

describe('chatheightestimator isHeightMismatchNotable', () => {
  it('is true for a large absolute and relative miss', () => {
    expect(isHeightMismatchNotable(100, 200)).toBe(true) // delta 100 >= max(24, 50)
  })
  it('is false for an exact match', () => {
    expect(isHeightMismatchNotable(100, 100)).toBe(false)
  })
  it('is false for small drift below both floors', () => {
    expect(isHeightMismatchNotable(100, 110)).toBe(false) // delta 10 < max(24, 27.5)
  })
  it('uses the 24px absolute floor for small rows', () => {
    expect(isHeightMismatchNotable(40, 70)).toBe(true) // delta 30 >= max(24, 17.5)
    expect(isHeightMismatchNotable(40, 58)).toBe(false) // delta 18 < 24
  })
  it('tolerates over-estimates more than under-estimates (bias-up is the safe direction)', () => {
    // Same 47px miss on a 175px row: an UNDER-estimate warns (>= 25% = 43.75),
    // but an OVER-estimate (the safe per-line ceil cushion) is tolerated (< 40% = 70).
    expect(isHeightMismatchNotable(128, 175)).toBe(true) // under by 47 >= max(24, 43.75)
    expect(isHeightMismatchNotable(222, 175)).toBe(false) // over by 47 < max(24, 70)
  })
  it('still flags a large structural over-estimate', () => {
    // A 961px guess for a 22px collapsed row clears even the looser 40% floor.
    expect(isHeightMismatchNotable(961, 22)).toBe(true) // over by 939 >= max(24, 8.8)
  })
})

describe('chatheightestimator warnIfHeightMismatch', () => {
  // rawMessage/content are now JSON values (objects), not strings, so devtools
  // renders them as expandable trees rather than escaped strings.
  const ctx = {
    state: { collapsed: false, expanded: true, toolBodyExpanded: false, diffView: 'unified' as const },
    rawMessage: { id: 'm1', content: '...' },
    content: { message: { content: [] } },
  }
  const breakdown = estimateRowHeight({ kind: 'tool_use:Edit', hasSpanLines: false, diffUnifiedRows: 4, diffHunkCount: 1, diffView: 'unified' }, CTX)

  it('warns once with the full payload when the divergence is notable', () => {
    const log = { warn: vi.fn() }
    const warned = warnIfHeightMismatch(log, 'm1', breakdown, breakdown.total + 200, ctx)
    expect(warned).toBe(true)
    expect(log.warn).toHaveBeenCalledTimes(1)
    const [msg, fields] = log.warn.mock.calls[0]
    expect(msg).toBe('chat row height estimate diverged')
    expect(fields).toMatchObject({
      id: 'm1',
      kind: breakdown.kind,
      estimated: breakdown.total,
      actual: breakdown.total + 200,
      state: ctx.state,
      rawMessage: ctx.rawMessage,
      content: ctx.content,
    })
    expect(fields.terms).toEqual(breakdown.terms)
    expect(typeof fields.deltaPx).toBe('number')
  })

  it('does not warn when the divergence is below threshold', () => {
    const log = { warn: vi.fn() }
    expect(warnIfHeightMismatch(log, 'm1', breakdown, breakdown.total + 5, ctx)).toBe(false)
    expect(log.warn).not.toHaveBeenCalled()
  })

  it('passes the raw message JSON value through verbatim (untruncated, no copy)', () => {
    const log = { warn: vi.fn() }
    const bigRaw = { content: 'x'.repeat(20000), nested: { deep: [1, 2, 3] } }
    warnIfHeightMismatch(log, 'm1', breakdown, breakdown.total + 300, { ...ctx, rawMessage: bigRaw })
    const fields = log.warn.mock.calls[0][1] as { rawMessage: unknown }
    // Same object reference -> no truncation, no transformation of the payload.
    expect(fields.rawMessage).toBe(bigRaw)
  })
})

// ---- feature extraction (buildHeightInput) ----

function parsed(over: Partial<ParsedMessageContent>): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: undefined, wrapper: null, ...over }
}

describe('chatheightestimator buildHeightInput', () => {
  const state = { collapsed: false, expanded: true, toolBodyExpanded: false, diffView: 'unified' as const }

  it('extracts prose text length and line count from a message envelope', () => {
    const p = parsed({ parentObject: { message: { content: [{ type: 'text', text: 'hello\nworld\n!' }] } } })
    const input = buildHeightInput({
      kind: 'assistant_text',
      hasSpanLines: false,
      category: { kind: 'assistant_text' } as MessageCategory,
      parsed: p,
      state,
    })
    expect(input.textLength).toBe('hello\nworld\n!'.length)
    expect(input.logicalLineCount).toBe(3)
  })

  it('renders a Claude Edit tool_use as a header, NOT a diff (the diff is on the result)', () => {
    // Real shape: toolUse is the whole {type,name,input} block. Edit/Write
    // tool_use rows render header-only; their diff renders on the tool_result.
    const category = { kind: 'tool_use', toolName: 'Edit', toolUse: { type: 'tool_use', name: 'Edit', input: { old_string: 'a\nb\nc', new_string: 'a\nB\nc' } }, content: [] } as MessageCategory
    const input = buildHeightInput({
      kind: 'tool_use:Edit',
      toolName: 'Edit',
      hasSpanLines: false,
      category,
      parsed: parsed({}),
      state: { ...state, diffView: 'split' },
    })
    expect(input.diffUnifiedRows).toBeUndefined()
    expect(input.diffSplitRows).toBeUndefined()
  })

  it('extracts diff rows from a Claude Edit tool_RESULT structuredPatch', () => {
    const structuredPatch = [{ oldStart: 1, oldLines: 2, newStart: 1, newLines: 2, lines: [' ctx', '-old', '+new', ' ctx2'] }]
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', tool_use_id: 't1', content: 'The file ... was updated' }] },
        tool_use_result: { filePath: 'a.ts', structuredPatch },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true, diffView: 'split' },
    })
    expect(input.diffUnifiedRows).toBe(4) // every hunk line renders
    expect(input.diffHunkCount).toBe(1)
    expect(input.diffAdded).toBe(1)
    expect(input.diffRemoved).toBe(1)
    expect(input.diffView).toBe('split')
    expect(input.collapsed).toBeUndefined() // diff path, not the collapsed-text path
    // Per-line content lengths exclude the +/-/space prefix (a separate span).
    expect(input.diffLineLengths).toEqual([' ctx'.length - 1, '-old'.length - 1, '+new'.length - 1, ' ctx2'.length - 1])
    // No originalFile -> only between-hunk separators (0 for a single hunk).
    expect(input.diffSeparatorRows).toBe(0)
  })

  it('counts leading + trailing gap separators for a mid-file Edit diff with originalFile', () => {
    // Hunk covers original lines 5-6 of a 20-line file: gap above (1-4) and below (7-20).
    const structuredPatch = [{ oldStart: 5, oldLines: 2, newStart: 5, newLines: 2, lines: [' a', '-b', '+c', ' d'] }]
    const originalFile = Array.from<string>({ length: 20 }).fill('x').join('\n')
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'updated' }] },
        tool_use_result: { tool_name: 'Edit', filePath: 'a.ts', structuredPatch, originalFile },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state,
    })
    expect(input.diffSeparatorRows).toBe(2) // leading gap + trailing gap
  })

  it('omits the leading/trailing gap separator when the hunk touches the file edge', () => {
    const buildSeparators = (oldStart: number, oldLines: number, fileLines: number): number | undefined => {
      const structuredPatch = [{ oldStart, oldLines, newStart: oldStart, newLines: oldLines, lines: Array.from<string>({ length: oldLines }).fill(' x') }]
      const originalFile = Array.from<string>({ length: fileLines }).fill('x').join('\n')
      const p = parsed({
        parentObject: {
          message: { content: [{ type: 'tool_result', content: 'updated' }] },
          tool_use_result: { tool_name: 'Edit', filePath: 'a.ts', structuredPatch, originalFile },
        },
      })
      return buildHeightInput({ kind: 'tool_result', hasSpanLines: false, category: { kind: 'tool_result' } as MessageCategory, parsed: p, state }).diffSeparatorRows
    }
    expect(buildSeparators(1, 2, 20)).toBe(1) // starts at line 1 -> no leading gap, only trailing
    expect(buildSeparators(19, 2, 20)).toBe(1) // reaches EOF (lines 19-20) -> no trailing gap, only leading
    expect(buildSeparators(1, 20, 20)).toBe(0) // whole file is one hunk -> no gaps either side
  })

  it('does not invent a trailing gap for a newline-terminated file', () => {
    // The renderer drops a single trailing-newline blank when counting the
    // original's lines (DiffViewer useGapData), so a hunk reaching the last
    // CONTENT line of a file that ends in '\n' shows NO trailing gap. A raw
    // newline count would see one extra line and add a phantom separator.
    const structuredPatch = [{ oldStart: 19, oldLines: 2, newStart: 19, newLines: 2, lines: [' a', '-b', '+c'] }]
    const originalFile = `${Array.from<string>({ length: 20 }).fill('x').join('\n')}\n` // 20 lines + trailing '\n'
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'updated' }] },
        tool_use_result: { tool_name: 'Edit', filePath: 'a.ts', structuredPatch, originalFile },
      },
    })
    const input = buildHeightInput({ kind: 'tool_result', hasSpanLines: false, category: { kind: 'tool_result' } as MessageCategory, parsed: p, state })
    expect(input.diffSeparatorRows).toBe(1) // leading gap (lines 1-18) only
  })

  it('counts a between-hunk separator only where lines are actually hidden', () => {
    const makeSeparators = (hunks: Array<{ oldStart: number, oldLines: number }>): number | undefined => {
      const structuredPatch = hunks.map(h => ({ ...h, newStart: h.oldStart, newLines: h.oldLines, lines: [' a', '-b', '+c'] }))
      const p = parsed({
        parentObject: {
          message: { content: [{ type: 'tool_result', content: 'x' }] },
          tool_use_result: { tool_name: 'Edit', filePath: 'a.ts', structuredPatch },
        },
      })
      return buildHeightInput({ kind: 'tool_result', hasSpanLines: false, category: { kind: 'tool_result' } as MessageCategory, parsed: p, state }).diffSeparatorRows
    }
    // Adjacent hunks (hunk 2 begins right after hunk 1) hide nothing between them.
    expect(makeSeparators([{ oldStart: 1, oldLines: 2 }, { oldStart: 3, oldLines: 2 }])).toBe(0)
    // A real gap (lines 3-9 hidden between the hunks) adds exactly one separator.
    expect(makeSeparators([{ oldStart: 1, oldLines: 2 }, { oldStart: 10, oldLines: 2 }])).toBe(1)
  })

  it('synthesizes a tool_RESULT diff from oldString/newString when no structuredPatch', () => {
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'updated' }] },
        tool_use_result: { filePath: 'a.ts', oldString: 'a\nb\nc', newString: 'a\nB\nc' },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state,
    })
    expect((input.diffUnifiedRows ?? 0)).toBeGreaterThan(0)
    expect((input.diffAdded ?? 0) + (input.diffRemoved ?? 0)).toBeGreaterThan(0)
  })

  it('sizes a collapsed Edit tool_result as a full diff, not the 59px collapsed-text cap', () => {
    // Regression: a tall Edit result (40 hunk lines) was mis-estimated as ~59px
    // collapsed text. Diffs are exempt from the 3-row cap, so it must be sized
    // as a full diff that dwarfs the collapsed-text estimate.
    const lines = Array.from({ length: 40 }, (_, i) => (i % 2 ? `+new ${i}` : `-old ${i}`))
    const structuredPatch = [{ oldStart: 1, oldLines: 20, newStart: 1, newLines: 20, lines }]
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'updated' }] },
        tool_use_result: { filePath: 'a.ts', structuredPatch },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    const total = estimateRowHeight(input, defaultHeightCtx(900)).total
    // 40 diff rows * 18px alone is far beyond the ~59px collapsed-text estimate.
    expect(total).toBeGreaterThan(600)
  })

  it('does NOT treat a non-Edit/Write tool_result (e.g. MultiEdit) as a diff', () => {
    // Only Edit/Write render diffs; a labeled MultiEdit result with a
    // structuredPatch still renders as text, so it must not size as a diff.
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'updated' }] },
        tool_use_result: { tool_name: 'MultiEdit', filePath: 'a.ts', structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-x', '+y'] }] },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.diffUnifiedRows).toBeUndefined()
    expect(input.collapsed).toBe(true) // collapsed-text path
  })

  it('still treats an Edit-labeled tool_result with structuredPatch as a diff', () => {
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'updated' }] },
        tool_use_result: { tool_name: 'Edit', filePath: 'a.ts', structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-x', '+y'] }] },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.diffUnifiedRows).toBe(2)
  })

  it('does NOT treat a failed edit (is_error) tool_result as a diff', () => {
    // A failed edit was not applied; it renders its error text, not a diff.
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', is_error: true, content: 'Error: file not found' }] },
        tool_use_result: { filePath: 'a.ts', structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-x', '+y'] }] },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.diffUnifiedRows).toBeUndefined()
    expect(input.collapsed).toBe(true) // fell through to the collapsed-text path
  })

  it('extracts an all-added diff from a Write-create tool_result (content + type create, empty structuredPatch)', () => {
    // A `Write` that creates a new file carries the whole body in `content`
    // with an EMPTY structuredPatch and no oldString/newString. The renderer
    // draws it as a full all-added diff (its result-side source has no diff, so
    // it falls back to the paired tool_use input's `content`), which the result
    // mirrors -- so it must size as a diff, not the collapsed-text body.
    const content = 'line 1\nline 2\nline 3\n'
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'File created successfully at: /tmp/new.ts' }] },
        tool_use_result: { type: 'create', filePath: '/tmp/new.ts', content, originalFile: null, structuredPatch: [] },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.diffUnifiedRows).toBe(3) // every line of the created body is an added row
    expect(input.diffAdded).toBe(3)
    expect(input.diffRemoved).toBe(0)
    expect(input.collapsed).toBeUndefined() // diff path, not the collapsed-text path
    expect(input.diffSeparatorRows).toBe(0) // all-added new file: no gap context
  })

  it('sizes a collapsed Write-create tool_result as a full diff, not the 36px collapsed-text cap', () => {
    // Regression for the reported 36px-vs-2004px divergence: a created ~110-line
    // test file was mis-estimated as a ~36px collapsed text body ("File created
    // successfully...") because the create path (content + empty structuredPatch,
    // no oldString/newString) was not recognized as a diff.
    const content = `${Array.from({ length: 110 }, (_, i) => `const x${i} = ${i}`).join('\n')}\n`
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'File created successfully at: /tmp/big.ts' }] },
        tool_use_result: { type: 'create', filePath: '/tmp/big.ts', content, originalFile: null, structuredPatch: [] },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    const total = estimateRowHeight(input, defaultHeightCtx(900)).total
    // 110 added rows * 18px alone dwarfs the ~36px collapsed-text estimate.
    expect(total).toBeGreaterThan(1800)
  })

  it('does NOT treat a tool_result that merely carries a `content` string as a diff (no create signal)', () => {
    // The all-added `content` fallback fires only for a Write create (`type:
    // 'create'`); a result that just carries a `content` string -- no `type`,
    // no structuredPatch, no old/newString -- is plain output, rendered as text.
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'ok' }] },
        tool_use_result: { filePath: '/tmp/a.ts', content: 'some\nmultiline\noutput' },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.diffUnifiedRows).toBeUndefined()
    expect(input.collapsed).toBe(true) // collapsed-text path
  })

  it('counts JSON lines for a hidden raw-JSON card', () => {
    const input = buildHeightInput({
      kind: 'hidden',
      hasSpanLines: false,
      category: { kind: 'hidden' } as MessageCategory,
      parsed: parsed({ rawText: '{\n  "a": 1,\n  "b": 2\n}' }),
      state,
    })
    expect(input.jsonLineCount).toBe(4)
  })

  it('counts MCP images in a tool result', () => {
    const p = parsed({
      parentObject: {
        message: {
          content: [{ type: 'tool_result', content: [{ type: 'image', source: {} }, { type: 'text', text: 'x' }, { type: 'image', source: {} }] }],
        },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state,
    })
    expect(input.imageCount).toBe(2)
  })

  it('carries the collapsed flag into a non-diff tool result', () => {
    const p = parsed({ parentObject: { message: { content: [{ type: 'tool_result', content: 'line1\nline2\nline3\nline4' }] } } })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.collapsed).toBe(true)
    expect(input.isError).toBe(false) // successful result -> no status header
    expect(input.hasHeader).toBe(false)
  })

  it('flags an errored tool_result so the estimate keeps the status header', () => {
    const p = parsed({ parentObject: { message: { content: [{ type: 'tool_result', is_error: true, content: 'boom' }] } } })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.isError).toBe(true)
    expect(input.hasHeader).toBe(true)
  })

  it('flags a sub-agent (Agent) result as having a status header even when successful', () => {
    // AgentResultView always wraps its body in a ToolStatusHeader ("Agent ... completed").
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', is_error: false, content: 'agent output' }] },
        tool_use_result: { agentId: 'a1', agentType: 'Explore', status: 'completed', content: [{ type: 'text', text: 'long output\n'.repeat(200) }] },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.isError).toBe(false)
    expect(input.hasHeader).toBe(true) // header despite success
  })

  it('flags a TaskOutput result (task payload) as having a status header', () => {
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'task output' }] },
        tool_use_result: { task: { task_id: 't1', status: 'completed', output: 'done' } },
      },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state: { ...state, collapsed: true },
    })
    expect(input.hasHeader).toBe(true)
  })

  it('extracts thinking text from an assistant_thinking message, not just text blocks', () => {
    // Regression: thinking content lives in a `type:"thinking"` block's `thinking`
    // field, NOT a `text` field. The old extractor read only `{text:'text'}`, so
    // every expanded thinking row was estimated as an empty 82px bubble.
    const thinking = 'reasoning line one\nreasoning line two\nreasoning line three'
    const p = parsed({ parentObject: { message: { content: [{ type: 'thinking', thinking, signature: 'sig' }] } } })
    const input = buildHeightInput({
      kind: 'assistant_thinking',
      hasSpanLines: false,
      category: { kind: 'assistant_thinking' } as MessageCategory,
      parsed: p,
      state: { collapsed: true, expanded: true, toolBodyExpanded: false, diffView: 'unified' },
    })
    expect(input.textLength).toBe(thinking.length)
    expect(input.logicalLineCount).toBe(3)
    expect(input.lineLengths).toEqual([18, 18, 20])
  })

  it('wires the expanded flag and per-line lengths for an agent_prompt', () => {
    const text = 'Task: do the thing\nwith details\nacross lines'
    const p = parsed({ parentObject: { type: 'user', parent_tool_use_id: 't1', message: { content: [{ type: 'text', text }] } } })
    const input = buildHeightInput({
      kind: 'agent_prompt',
      hasSpanLines: false,
      category: { kind: 'agent_prompt' } as MessageCategory,
      parsed: p,
      state: { collapsed: true, expanded: false, toolBodyExpanded: false, diffView: 'unified' },
    })
    expect(input.expanded).toBe(false)
    expect(input.lineLengths).toEqual([18, 12, 12])
  })

  it('populates per-line lengths for prose so the engine can sum per-line wraps', () => {
    const body = 'short\na much longer line that will wrap when the bubble is narrow enough'
    const p = parsed({ parentObject: { message: { content: [{ type: 'text', text: body }] } } })
    const input = buildHeightInput({
      kind: 'assistant_text',
      hasSpanLines: false,
      category: { kind: 'assistant_text' } as MessageCategory,
      parsed: p,
      state,
    })
    expect(input.lineLengths).toEqual([5, body.length - 6])
  })

  it('caps per-line lengths for a pathologically long body (folds the remainder)', () => {
    // A pasted 3000-line log must not allocate an unbounded per-row array; the
    // tail folds into one trailing virtual line so its wrap rows still count.
    const body = Array.from<string>({ length: 3000 }).fill('x').join('\n')
    const p = parsed({ parentObject: { message: { content: [{ type: 'text', text: body }] } } })
    const input = buildHeightInput({
      kind: 'assistant_text',
      hasSpanLines: false,
      category: { kind: 'assistant_text' } as MessageCategory,
      parsed: p,
      state,
    })
    expect(input.lineLengths!.length).toBe(2001) // MAX_LINE_SAMPLES (2000) + 1 folded remainder
    expect(input.lineLengths![2000]).toBeGreaterThan(900) // remainder ~1000 folded lines
  })

  it('floors a folded long-body estimate at the true hard-line count', () => {
    // The folded tail (one entry summing ~1000 lines' chars) alone sizes as a
    // single ~25-row wrapping line, under-estimating a 3000-line body by ~1000
    // rows. An under-estimate above the scroll anchor accumulates into upward
    // drift, so the estimate must floor at the true hard-line count instead.
    const body = Array.from<string>({ length: 3000 }).fill('x').join('\n')
    const p = parsed({ parentObject: { message: { content: [{ type: 'text', text: body }] } } })
    const input = buildHeightInput({
      kind: 'assistant_text',
      hasSpanLines: false,
      category: { kind: 'assistant_text' } as MessageCategory,
      parsed: p,
      state,
    })
    const breakdown = estimateRowHeight(input, CTX)
    // 3000 hard lines each render >= 1 row -- the estimate reflects that, not the
    // ~2025 rows the capped per-line array alone would yield.
    expect(breakdown.metrics.visualRows).toBe(3000)
    expect(breakdown.total).toBeGreaterThanOrEqual(3000 * CTX.proseLinePx)
  })

  it('drops a malformed structuredPatch hunk instead of throwing', () => {
    // structuredPatch is external wire data. A hunk missing the `lines` array
    // would throw (h.lines.length) inside the estimator's offset-map memo and
    // blank the WHOLE virtualized list, so a malformed entry must be dropped and
    // the row sized as a plain text result instead.
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'File edited' }] },
        tool_use_result: { tool_name: 'Edit', structuredPatch: [{ oldStart: 1 }] },
      },
    })
    const build = () => buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: p,
      state,
    })
    expect(build).not.toThrow()
    // Malformed patch dropped -> no diff metrics; falls back to the text body.
    expect(build().diffUnifiedRows).toBeUndefined()
  })

  it('extracts the Bash command first line as the tool_use header summary', () => {
    // Regression: the Bash command renders as a summary line under the header but
    // lives in `toolUse.command`, not a text block, so the header was estimated
    // as a bare 22px line.
    const category = {
      kind: 'tool_use',
      toolName: 'Bash',
      // Real shape: the command lives at toolUse.input.command (toolUse is the
      // whole {type,name,input} block), one level deeper than it looks.
      toolUse: { type: 'tool_use', name: 'Bash', input: { command: 'grep -rn "needle" src/ | head\n# trailing line', description: 'Search' } },
      content: [],
    } as MessageCategory
    const input = buildHeightInput({
      kind: 'tool_use:Bash',
      toolName: 'Bash',
      hasSpanLines: false,
      category,
      parsed: parsed({}),
      state,
    })
    expect(input.textLength).toBe('grep -rn "needle" src/ | head'.length)
    expect(input.logicalLineCount).toBe(1) // only the first non-empty line is shown
  })
})

describe('chatheightestimator tool_use result body (estimateToolUseBody)', () => {
  const longMono = { textLength: 2000, logicalLineCount: 40, lineLengths: Array.from<number>({ length: 40 }).fill(50) }

  it('sizes a tool_use result body via the collapse model, not a bare header', () => {
    const header = est({ kind: 'tool_use:commandExecution', hasSpanLines: false })
    const body = est({ kind: 'tool_use:commandExecution', hasSpanLines: false, toolUseRendersResultBody: true, bodyMarkdown: false, collapsed: false, ...longMono })
    expect(body).toBeGreaterThan(header) // the full body is sized, not just a header line
  })

  it('clamps a collapsed result body to the collapsed cap', () => {
    const base = { kind: 'tool_use:commandExecution', hasSpanLines: false, toolUseRendersResultBody: true, bodyMarkdown: false, ...longMono } as const
    const expanded = est({ ...base, collapsed: false })
    const collapsed = est({ ...base, collapsed: true })
    expect(collapsed).toBeLessThan(expanded) // collapsed clamps to ~3 rows / the cap
  })

  it('adds a tool-title header line for a ToolUseLayout-based row (toolHeaderLine)', () => {
    const base = { kind: 'tool_use:mcpTool', hasSpanLines: false, toolUseRendersResultBody: true, bodyMarkdown: true, uncollapsed: true, ...longMono } as const
    expect(est({ ...base, toolHeaderLine: true })).toBeGreaterThan(est({ ...base }))
  })

  it('does not clamp an uncollapsed (alwaysVisible) body even when collapsed is set', () => {
    const base = { kind: 'tool_use:mcpTool', hasSpanLines: false, toolUseRendersResultBody: true, bodyMarkdown: true, collapsed: true, ...longMono } as const
    expect(est({ ...base, uncollapsed: true })).toBeGreaterThan(est({ ...base }))
  })
})

describe('chatheightestimator tool_result + diff chrome', () => {
  it('sizes a successful collapsed text tool_result body-only (no status header)', () => {
    // A successful command/Read/Grep result renders headerless; an error or a
    // sub-agent/task result (hasHeader) adds a status header. Body = min(3, cap) = 54.
    const success = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, textLength: 388, logicalLineCount: 10 })
    const withHeader = est({ kind: 'tool_result', hasSpanLines: false, collapsed: true, hasHeader: true, textLength: 388, logicalLineCount: 10 })
    expect(success).toBe(54) // collapsed body only, NO phantom 22.4 header
    expect(withHeader).toBe(77) // + the 22.4 status header line (ceil 76.4)
  })

  it('does not add a status header to a tool_result diff, but a tool_use diff keeps one', () => {
    const resultDiff = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, diffView: 'unified', diffUnifiedRows: 4, diffHunkCount: 1, diffLineLengths: [4, 4, 4, 4], diffSeparatorRows: 0 }, CTX)
    expect(resultDiff.terms.find(t => t.label === 'header line')).toBeUndefined()
    const toolUseDiff = estimateRowHeight({ kind: 'tool_use:apply_patch', hasSpanLines: false, diffView: 'unified', diffUnifiedRows: 4, diffHunkCount: 1 }, CTX)
    expect(toolUseDiff.terms.find(t => t.label === 'header line')).toBeDefined()
  })

  it('wraps long unified diff lines into multiple rows', () => {
    const ctx = defaultHeightCtx(300) // narrow so the long line wraps
    const base = { kind: 'tool_result' as const, hasSpanLines: false, diffView: 'unified' as const, diffUnifiedRows: 3, diffHunkCount: 1, diffSeparatorRows: 0 }
    const shortLines = estimateRowHeight({ ...base, diffLineLengths: [5, 5, 5] }, ctx)
    const longLines = estimateRowHeight({ ...base, diffLineLengths: [5, 600, 5] }, ctx)
    expect(longLines.total).toBeGreaterThan(shortLines.total)
    expect(longLines.metrics.rows as number).toBeGreaterThan(3) // the 600-char line wrapped
    expect(shortLines.metrics.rows).toBe(3)
  })

  it('adds a gap-separator term per context gap', () => {
    const base = { kind: 'tool_result' as const, hasSpanLines: false, diffView: 'unified' as const, diffUnifiedRows: 4, diffHunkCount: 1, diffLineLengths: [4, 4, 4, 4] }
    const noGaps = estimateRowHeight({ ...base, diffSeparatorRows: 0 }, CTX)
    const twoGaps = estimateRowHeight({ ...base, diffSeparatorRows: 2 }, CTX)
    expect(twoGaps.total - noGaps.total).toBe(2 * CTX.diffSeparatorPx)
  })
})

describe('chatheightestimator per-line wrap model', () => {
  it('sums per-line wrap counts for mixed short+long prose, not a flat max', () => {
    // 5 short lines (1 row each) + 1 long line that wraps to several rows. The
    // old max(logicalLineCount, totalWrap) model under-counts because the short
    // lines do not pack into the long line's wasted row space.
    const ctx = defaultHeightCtx(400)
    const lineLengths = [2, 2, 2, 2, 2, 800]
    const textLength = lineLengths.reduce((s, n) => s + n, 0) + lineLengths.length - 1
    const perLine = estimateRowHeight({ kind: 'assistant_text', hasSpanLines: false, lineLengths, textLength, logicalLineCount: lineLengths.length }, ctx)
    const flat = estimateRowHeight({ kind: 'assistant_text', hasSpanLines: false, textLength, logicalLineCount: lineLengths.length }, ctx)
    expect(perLine.total).toBeGreaterThan(flat.total)
    // The long line alone wraps to ceil(800*8/(400*0.85-32)) = ceil(800*8/308) = 21 rows.
    expect(perLine.metrics.visualRows).toBe(5 + 21)
  })

  it('adds exactly one block-gap margin per interior blank line, not a full row', () => {
    const ctx = defaultHeightCtx(800)
    // Two text rows in both cases; the only difference is the interior blank line.
    const twoBlocks = estimateRowHeight({ kind: 'assistant_text', hasSpanLines: false, lineLengths: [4, 0, 4], textLength: 9, logicalLineCount: 3 }, ctx)
    const oneBlock = estimateRowHeight({ kind: 'assistant_text', hasSpanLines: false, lineLengths: [4, 4], textLength: 8, logicalLineCount: 2 }, ctx)
    expect(twoBlocks.metrics.visualRows).toBe(2) // blank line is NOT a text row
    expect(twoBlocks.metrics.blockGaps).toBe(1)
    // The blank line adds one ~16px block gap, far less than a 25.6px text row.
    expect(twoBlocks.total - oneBlock.total).toBe(ctx.blockGapPx)
  })

  it('does not count leading or trailing blank lines as block gaps', () => {
    const ctx = defaultHeightCtx(800)
    const padded = estimateRowHeight({ kind: 'assistant_text', hasSpanLines: false, lineLengths: [0, 4, 0], textLength: 6, logicalLineCount: 3 }, ctx)
    expect(padded.metrics.visualRows).toBe(1)
    expect(padded.metrics.blockGaps).toBe(0)
  })

  it('applies the per-line model to expanded thinking too', () => {
    const ctx = defaultHeightCtx(400)
    const lineLengths = [2, 2, 2, 800]
    const withLines = estimateRowHeight({ kind: 'assistant_thinking', hasSpanLines: false, expanded: true, lineLengths, textLength: 806, logicalLineCount: 4 }, ctx)
    const flat = estimateRowHeight({ kind: 'assistant_thinking', hasSpanLines: false, expanded: true, textLength: 806, logicalLineCount: 4 }, ctx)
    expect(withLines.total).toBeGreaterThan(flat.total)
  })
})

describe('chatheightestimator renderer-fidelity fixes', () => {
  const state = { collapsed: false, expanded: true, toolBodyExpanded: false, diffView: 'unified' as const }

  it('counts a status header for an interrupted command result (is_error false)', () => {
    // Ctrl-C: the tool_result block reports is_error false, but the
    // tool_use_result carries interrupted -- the renderer still shows a header.
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: 'aborted output', is_error: false }] },
        tool_use_result: { interrupted: true },
      },
    })
    const input = buildHeightInput({ kind: 'tool_result', hasSpanLines: false, category: { kind: 'tool_result' } as MessageCategory, parsed: p, state })
    expect(input.hasHeader).toBe(true)
    const withHeader = est({ kind: 'tool_result', hasSpanLines: false, textLength: 14, logicalLineCount: 1, hasHeader: true })
    const headerless = est({ kind: 'tool_result', hasSpanLines: false, textLength: 14, logicalLineCount: 1, hasHeader: false })
    expect(withHeader).toBeGreaterThan(headerless)
  })

  it('counts the text body alongside images in an MCP result', () => {
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', content: [{ type: 'text', text: 'here is the page' }, { type: 'image' }] }] },
      },
    })
    const input = buildHeightInput({ kind: 'tool_result', hasSpanLines: false, category: { kind: 'tool_result' } as MessageCategory, parsed: p, state })
    expect(input.imageCount).toBe(1)
    expect(input.textLength).toBeGreaterThan(0)
    // Text-plus-image is taller than the image alone (the dropped body height).
    const both = est({ kind: 'tool_result', hasSpanLines: false, imageCount: 1, textLength: 200, logicalLineCount: 4 })
    const imageOnly = est({ kind: 'tool_result', hasSpanLines: false, imageCount: 1 })
    expect(both).toBeGreaterThan(imageOnly)
  })

  it('extracts the TodoWrite checklist count and grows the estimate per todo', () => {
    const category = { kind: 'tool_use', toolName: 'TodoWrite', toolUse: { type: 'tool_use', name: 'TodoWrite', input: { todos: [{ content: 'a' }, { content: 'b' }, { content: 'c' }] } }, content: [] } as unknown as MessageCategory
    const input = buildHeightInput({ kind: 'tool_use:TodoWrite', toolName: 'TodoWrite', hasSpanLines: false, category, parsed: parsed({}), state })
    expect(input.todoCount).toBe(3)
    const three = est({ kind: 'tool_use:TodoWrite', hasSpanLines: false, todoCount: 3 })
    const empty = est({ kind: 'tool_use:TodoWrite', hasSpanLines: false, todoCount: 0 })
    expect(empty).toBeLessThan(30) // empty list -> the "cleared" placeholder header
    expect(three).toBeGreaterThan(empty + 3 * 20) // ~3 todo rows of body
  })

  it('extracts the ExitPlanMode plan markdown and sizes its body', () => {
    const planText = 'Step 1\n\nStep 2 with quite a long line that will wrap a few times across the body'
    const category = { kind: 'tool_use', toolName: 'ExitPlanMode', toolUse: { type: 'tool_use', name: 'ExitPlanMode', input: { plan: planText } }, content: [] } as unknown as MessageCategory
    const input = buildHeightInput({ kind: 'tool_use:ExitPlanMode', toolName: 'ExitPlanMode', hasSpanLines: false, category, parsed: parsed({}), state })
    expect(input.textLength).toBe(planText.length)
    expect(input.lineLengths).toBeDefined()
    const withPlan = estimateRowHeight(input, CTX).total
    const headerOnly = est({ kind: 'tool_use:ExitPlanMode', hasSpanLines: false })
    expect(withPlan).toBeGreaterThan(headerOnly)
  })

  it('sizes a task_notification as a tool header, not a centered prose bubble', () => {
    const taskNotif = estimateRowHeight({ kind: 'task_notification', hasSpanLines: false }, CTX)
    expect(taskNotif.total).toBeLessThan(30) // a header line, no bubble chrome
    expect(taskNotif.terms.some(t => t.label.includes('bubble'))).toBe(false)
    // Contrast: a real notification keeps the centered bubble chrome.
    const notif = estimateRowHeight({ kind: 'notification', hasSpanLines: false, childCount: 1 }, CTX)
    expect(notif.terms.some(t => t.label.includes('bubble'))).toBe(true)
  })

  it('extracts the Grep path as a one-line summary', () => {
    const path = 'src/very/long/path/to/some/file.ts'
    const category = { kind: 'tool_use', toolName: 'Grep', toolUse: { type: 'tool_use', name: 'Grep', input: { path, pattern: 'x' } }, content: [] } as unknown as MessageCategory
    const input = buildHeightInput({ kind: 'tool_use:Grep', toolName: 'Grep', hasSpanLines: false, category, parsed: parsed({}), state })
    expect(input.textLength).toBe(path.length)
    expect(input.logicalLineCount).toBe(1)
  })

  it('relativizes the Grep path summary against workingDir (matches the renderer)', () => {
    // The renderer shows relativizePath(path, workingDir, homeDir); the estimate must
    // size that shorter relativized form, not the longer absolute path, or a deep path
    // over-estimates the one-line summary and sizes the off-screen row too tall.
    const abs = '/Users/me/project/src/deep/file.ts'
    const category = { kind: 'tool_use', toolName: 'Grep', toolUse: { type: 'tool_use', name: 'Grep', input: { path: abs } }, content: [] } as unknown as MessageCategory
    const input = buildHeightInput({ kind: 'tool_use:Grep', toolName: 'Grep', hasSpanLines: false, category, parsed: parsed({}), state, workingDir: '/Users/me/project' })
    expect(input.textLength).toBe('src/deep/file.ts'.length)
    expect(input.textLength).toBeLessThan(abs.length)
    expect(input.logicalLineCount).toBe(1)
  })
})

describe('chatheightestimator estimator-fidelity S2-S6', () => {
  const state = { collapsed: false, expanded: true, toolBodyExpanded: false, diffView: 'unified' as const }

  it('sizes a notification at the 14px line, not the 16px prose line (S5)', () => {
    const n = estimateRowHeight({ kind: 'notification', hasSpanLines: false, childCount: 1, textLength: 10, logicalLineCount: 1 }, CTX)
    const rowTerm = n.terms.find(t => t.label.includes('rows across'))
    expect(rowTerm).toBeDefined()
    expect(rowTerm!.value).toBeCloseTo(CTX.toolLinePx, 5) // 1 row * 14px line (22.4), not 25.6
  })

  it('wraps a long notification body to multiple rows with the proportional advance (bias up)', () => {
    // A long single logical line must wrap to several visual rows -- charged with the
    // PROPORTIONAL proseAvgCharPx, not the narrower 12px monoAvgCharPx (which fits
    // more chars per row, under-counts wraps, and biases the estimate DOWN).
    const short = estimateRowHeight({ kind: 'notification', hasSpanLines: false, childCount: 1, textLength: 20, logicalLineCount: 1 }, CTX)
    const long = estimateRowHeight({ kind: 'notification', hasSpanLines: false, childCount: 1, textLength: 2000, logicalLineCount: 1 }, CTX)
    const longRows = long.terms.find(t => t.label.includes('rows across'))
    // The wrap math ran: 2000 chars at the proportional advance is many rows, each a
    // 14px line, so the long body is well taller than the single-row short one.
    expect(longRows!.value).toBeGreaterThan(CTX.toolLinePx * 5)
    expect(long.total).toBeGreaterThan(short.total)
    // Pin the PROPORTIONAL advance specifically: recompute the wrap rows under both
    // candidate char widths (mirrors visualRows()/wrapRowsForLine()/proseTextWidth()).
    // proseAvgCharPx (wider) yields strictly more rows than the 7.2px monoAvgCharPx at
    // 2000 chars, and the emitted term must match the PROSE count -- a regression back
    // to monoAvgCharPx would match the smaller mono count instead.
    const width = Math.max(1, CTX.contentWidthPx * CTX.bubbleMaxWidthFrac - CTX.bubblePadH)
    const wrapRows = (charPx: number) => Math.max(1, Math.ceil((2000 * charPx) / Math.max(charPx * 4, width)))
    const rowsProse = wrapRows(CTX.proseAvgCharPx)
    const rowsMono = wrapRows(CTX.monoAvgCharPx)
    expect(rowsProse).toBeGreaterThan(rowsMono) // the two calibrations diverge at 2000 chars
    expect(longRows!.value).toBeCloseTo(rowsProse * CTX.toolLinePx, 5)
    expect(longRows!.value).not.toBeCloseTo(rowsMono * CTX.toolLinePx, 5)
  })

  it('sizes the <pre> detail of an error result_divider, but not a plain one (S6)', () => {
    const errParsed = parsed({ topLevel: { type: 'result', is_error: true, subtype: 'error_max_turns', errors: ['line one', 'line two'] } })
    const errInput = buildHeightInput({ kind: 'result_divider', hasSpanLines: false, category: { kind: 'result_divider' } as MessageCategory, parsed: errParsed, state })
    expect(errInput.logicalLineCount).toBe(2)
    const withDetail = estimateRowHeight(errInput, CTX)
    const bare = estimateRowHeight({ kind: 'result_divider', hasSpanLines: false }, CTX)
    expect(withDetail.total).toBeGreaterThan(bare.total)
    expect(withDetail.terms.some(t => t.label.includes('error detail'))).toBe(true)

    // A non-error (success) divider extracts no detail.
    const okParsed = parsed({ topLevel: { type: 'result', is_error: false, subtype: 'success' } })
    const okInput = buildHeightInput({ kind: 'result_divider', hasSpanLines: false, category: { kind: 'result_divider' } as MessageCategory, parsed: okParsed, state })
    expect(okInput.logicalLineCount ?? 0).toBe(0)
  })

  it('wraps a long single-line error detail past one row (no newline) (S6b)', () => {
    // The error <pre> is pre-wrap + word-break, so a single long line WRAPS. The
    // estimate must size it from textLength, not just the logical \n count -- charging
    // one row would under-estimate (the offset-map-drifting direction this file avoids).
    const longLine = 'x'.repeat(600)
    const longParsed = parsed({ topLevel: { type: 'result', is_error: true, subtype: 'error_during_execution', errors: [longLine] } })
    const longInput = buildHeightInput({ kind: 'result_divider', hasSpanLines: false, category: { kind: 'result_divider' } as MessageCategory, parsed: longParsed, state })
    expect(longInput.logicalLineCount).toBe(1) // ONE hard line...
    const long = estimateRowHeight(longInput, CTX)
    expect(long.terms.some(t => t.label.includes('error detail'))).toBe(true)
    expect(long.metrics.errorDetailRows as number).toBeGreaterThan(1) // ...wrapped to many rows.

    // A short single-line detail (same logical line count) is sized to fewer rows, so
    // the long line is strictly taller -- proving textLength drives the wrap, not \n count.
    const shortParsed = parsed({ topLevel: { type: 'result', is_error: true, subtype: 'error_during_execution', errors: ['oops'] } })
    const shortInput = buildHeightInput({ kind: 'result_divider', hasSpanLines: false, category: { kind: 'result_divider' } as MessageCategory, parsed: shortParsed, state })
    expect(shortInput.logicalLineCount).toBe(1)
    expect(estimateRowHeight(shortInput, CTX).total).toBeLessThan(long.total)
  })

  it('counts the full command for an expanded multi-line Bash, one line collapsed (S4)', () => {
    const command = 'echo a\necho b\necho c'
    const category = { kind: 'tool_use', toolName: 'Bash', toolUse: { type: 'tool_use', name: 'Bash', input: { command } }, content: [] } as unknown as MessageCategory
    const collapsed = buildHeightInput({ kind: 'tool_use:Bash', toolName: 'Bash', hasSpanLines: false, category, parsed: parsed({}), state: { ...state, toolBodyExpanded: false } })
    expect(collapsed.logicalLineCount).toBe(1) // first-line summary
    const expanded = buildHeightInput({ kind: 'tool_use:Bash', toolName: 'Bash', hasSpanLines: false, category, parsed: parsed({}), state: { ...state, toolBodyExpanded: true } })
    expect(expanded.logicalLineCount).toBe(3) // full command body
    expect(estimateRowHeight(expanded, CTX).total).toBeGreaterThan(estimateRowHeight(collapsed, CTX).total)
  })

  it('sizes an Agent markdown result body taller than a plain monospace one, with block gaps (S2)', () => {
    const body = 'para one\n\npara two'
    const md = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: parsed({ parentObject: { message: { content: [{ type: 'tool_result', content: body }] }, tool_use_result: { agentId: 'sub-1' } } }),
      state: { ...state, collapsed: false },
    })
    expect(md.bodyMarkdown).toBe(true)
    const mdEst = estimateRowHeight(md, CTX)
    expect(mdEst.metrics.markdown).toBe(true)
    expect(mdEst.terms.some(t => t.label.includes('block gaps'))).toBe(true) // 2 paragraphs -> 1 gap

    const mono = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' } as MessageCategory,
      parsed: parsed({ parentObject: { message: { content: [{ type: 'tool_result', content: body }] } } }),
      state: { ...state, collapsed: false },
    })
    expect(mono.bodyMarkdown).toBe(false)
    expect(mdEst.total).toBeGreaterThan(estimateRowHeight(mono, CTX).total)
  })

  it('models the AskUserQuestion question/option body, growing with options (S3)', () => {
    const category = {
      kind: 'tool_use',
      toolName: 'AskUserQuestion',
      toolUse: { type: 'tool_use', name: 'AskUserQuestion', input: { questions: [{ header: 'Q1', options: [{ label: 'a' }, { label: 'b' }] }, { header: 'Q2', options: [{ label: 'c' }] }] } },
      content: [],
    } as unknown as MessageCategory
    const input = buildHeightInput({ kind: 'tool_use:AskUserQuestion', toolName: 'AskUserQuestion', hasSpanLines: false, category, parsed: parsed({}), state })
    expect(input.askQuestionCount).toBe(2)
    expect(input.askOptionCount).toBe(3)
    const headerOnly = estimateRowHeight({ kind: 'tool_use:AskUserQuestion', hasSpanLines: false }, CTX)
    expect(estimateRowHeight(input, CTX).total).toBeGreaterThan(headerOnly.total)
    const more = estimateRowHeight({ kind: 'tool_use:AskUserQuestion', hasSpanLines: false, askQuestionCount: 1, askOptionCount: 5 }, CTX)
    const fewer = estimateRowHeight({ kind: 'tool_use:AskUserQuestion', hasSpanLines: false, askQuestionCount: 1, askOptionCount: 2 }, CTX)
    expect(more.total).toBeGreaterThan(fewer.total)
  })

  it('sizes a Task* card (TaskCreate/TaskUpdate/TaskGet) as header + a body baseline', () => {
    // Old model was header-only (~23px); WARN data shows these cards measure
    // ~49-68px (subject header + a 1-2 line description from the todo store).
    const update = est({ kind: 'tool_use:TaskUpdate', hasSpanLines: false })
    expect(update).toBeGreaterThan(45)
    expect(update).toBeLessThan(70)
    // The baseline lands inside the observed band, so neither the 49 nor the 68
    // cluster trips the WARN floors.
    expect(isHeightMismatchNotable(update, 49)).toBe(false)
    expect(isHeightMismatchNotable(update, 68)).toBe(false)
    // TaskCreate and TaskGet route through the same card model.
    expect(est({ kind: 'tool_use:TaskCreate', hasSpanLines: false })).toBe(update)
    expect(est({ kind: 'tool_use:TaskGet', hasSpanLines: false })).toBe(update)
  })
})

describe('chatheightestimator custom result renderers', () => {
  it('charges a summary prompt line (toolLinePx + askPromptMarginPx) for summaryLineCount', () => {
    const r = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, textLength: 10, logicalLineCount: 1, summaryLineCount: 1 }, CTX)
    const summary = r.terms.find(t => t.label.includes('summary'))
    expect(summary!.value).toBeCloseTo(CTX.toolLinePx + CTX.askPromptMarginPx, 5) // 26.4
  })

  it('keeps the summary line OUTSIDE the body collapse (unmasked sibling)', () => {
    const r = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, collapsed: true, textLength: 500, logicalLineCount: 20, lineLengths: Array.from<number>({ length: 20 }).fill(10), summaryLineCount: 1 }, CTX)
    expect(r.terms.some(t => t.label.includes('summary'))).toBe(true)
    expect(r.terms.some(t => t.label.includes('collapsed body'))).toBe(true)
  })

  it('does NOT collapse an uncollapsed body, however many logical lines (MCP/ToolSearch)', () => {
    const lineLengths = Array.from<number>({ length: 20 }).fill(10)
    const base = { kind: 'tool_result' as const, hasSpanLines: false, collapsed: true, textLength: 200, logicalLineCount: 20, lineLengths }
    const collapsed = estimateRowHeight(base, CTX).total
    const uncollapsed = estimateRowHeight({ ...base, uncollapsed: true }, CTX).total
    expect(collapsed).toBeLessThan(CTX.collapsedCapPx + 1) // clamped to ~57.6
    expect(uncollapsed).toBeCloseTo(20 * CTX.monoLinePx, 5) // full 20 rows, no clamp
  })

  it('widens the collapse gate with collapsedRowThreshold (Bash \\r progress)', () => {
    const lineLengths = Array.from<number>({ length: 5 }).fill(10)
    const base = { kind: 'tool_result' as const, hasSpanLines: false, collapsed: true, textLength: 50, logicalLineCount: 5, lineLengths }
    const def = estimateRowHeight(base, CTX).total
    const widened = estimateRowHeight({ ...base, collapsedRowThreshold: 7 }, CTX).total
    expect(def).toBeLessThan(CTX.collapsedCapPx + 1) // 5 > 3 -> collapses (~54)
    expect(widened).toBeCloseTo(5 * CTX.monoLinePx, 5) // 5 <= 7 -> full 90
  })

  it('sizes a jsonBody at jsonLinePx (19.2), not monoLinePx (18)', () => {
    const base = { kind: 'tool_result' as const, hasSpanLines: false, textLength: 50, logicalLineCount: 5, lineLengths: Array.from<number>({ length: 5 }).fill(10) }
    const body = estimateRowHeight({ ...base, jsonBody: true }, CTX).terms.find(t => t.label.includes('body'))
    expect(body!.value).toBeCloseTo(5 * CTX.jsonLinePx, 5) // 96, not 90
  })

  it('adds always-visible MCP Arguments / Structured pre-blocks (label + mono rows)', () => {
    const r = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, textLength: 10, logicalLineCount: 1, bodyMarkdown: true, uncollapsed: true, argsLineCount: 4, structuredLineCount: 2 }, CTX)
    expect(r.terms.find(t => t.label === 'mcp args label')!.value).toBeCloseTo(CTX.jsonLinePx, 5)
    expect(r.terms.find(t => t.label.startsWith('mcp args') && t.label.includes('rows'))!.value).toBeCloseTo(4 * CTX.monoLinePx, 5)
    expect(r.terms.find(t => t.label.startsWith('mcp structured') && t.label.includes('rows'))!.value).toBeCloseTo(2 * CTX.monoLinePx, 5)
  })

  it('charges a status header for an image result ONLY when hasHeader (no phantom MCP header)', () => {
    const noHeader = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, imageCount: 1 }, CTX)
    const withHeader = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, imageCount: 1, hasHeader: true }, CTX)
    expect(noHeader.terms.some(t => t.label.includes('header'))).toBe(false)
    expect(withHeader.terms.some(t => t.label.includes('header'))).toBe(true)
  })

  it('does NOT clamp an uncollapsed (MCP) image result to the collapse cap', () => {
    const clamped = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, collapsed: true, imageCount: 2 }, CTX).total
    const mcp = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, collapsed: true, uncollapsed: true, imageCount: 2 }, CTX).total
    expect(clamped).toBeLessThan(100) // ~57.6 cap
    expect(mcp).toBeGreaterThan(600) // 2 * ~328, full
  })

  it('adds the MCP Arguments pre-block on the IMAGE path too, with no phantom header', () => {
    const withArgs = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, uncollapsed: true, imageCount: 1, argsLineCount: 4 }, CTX)
    expect(withArgs.terms.find(t => t.label === 'mcp args label')!.value).toBeCloseTo(CTX.jsonLinePx, 5)
    expect(withArgs.terms.find(t => t.label.startsWith('mcp args') && t.label.includes('rows'))!.value).toBeCloseTo(4 * CTX.monoLinePx, 5)
    expect(withArgs.terms.some(t => t.label.includes('header'))).toBe(false) // McpToolCallBody draws no header
  })

  it('omits the body row for an empty tool_result body (header + summary only)', () => {
    const r = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, hasHeader: true, summaryLineCount: 1, textLength: 0, logicalLineCount: 0, lineLengths: [] }, CTX)
    expect(r.terms.some(t => t.label.includes('body'))).toBe(false)
    expect(r.total).toBe(49) // header 22.4 + summary 26.4 -> ceil 49
  })

  it('sizes a WebSearch result as summary + never-wrapping link rows + 2px gaps', () => {
    const two = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, webSearchLinkCount: 2 }, CTX)
    expect(two.terms.find(t => t.label === 'results summary')!.value).toBeCloseTo(CTX.toolLinePx + CTX.askPromptMarginPx, 5)
    expect(two.terms.find(t => t.label.includes('link rows'))!.value).toBeCloseTo(2 * CTX.monoLinePx + CTX.webSearchRowGapPx, 5) // 38
  })

  it('collapses a WebSearch link list by ITEM count, clamping the list to the cap', () => {
    const collapsed = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, collapsed: true, webSearchLinkCount: 5 }, CTX)
    // 5 links > 3 -> show 3 rows (3*18 + 2*2 = 58), clamped to collapsedCapPx 57.6.
    expect(collapsed.terms.find(t => t.label.includes('link rows'))!.value).toBeCloseTo(CTX.collapsedCapPx, 5)
    // The summary still sits above the clamped list.
    expect(collapsed.terms.some(t => t.label === 'results summary')).toBe(true)
  })

  it('adds the WebSearch markdown summary body only when expanded', () => {
    const collapsed = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, collapsed: true, webSearchLinkCount: 2, textLength: 200, logicalLineCount: 1, lineLengths: [200] }, CTX)
    const expanded = estimateRowHeight({ kind: 'tool_result', hasSpanLines: false, webSearchLinkCount: 2, textLength: 200, logicalLineCount: 1, lineLengths: [200] }, CTX)
    expect(expanded.total).toBeGreaterThan(collapsed.total) // the revealed summary markdown
  })
})

describe('chatheightestimator buildEstimateKey', () => {
  const base = { seq: 5n, hasToolUseSibling: false, toolUseContentVersion: 0, uiVersion: 0, contentVersion: 0, hasCommandStream: false }

  it('pins the pipe-joined format (a change here is a cache-key migration)', () => {
    expect(buildEstimateKey({ seq: 5n, hasToolUseSibling: true, toolUseContentVersion: 4, uiVersion: 2, contentVersion: 3, hasCommandStream: true }))
      .toBe('5|s|4|2|3|c')
    // hasToolUseSibling + hasCommandStream false collapse to empty markers.
    expect(buildEstimateKey(base)).toBe('5||0|0|0|')
  })

  it('changes when any field a row\'s height depends on changes', () => {
    const k = buildEstimateKey(base)
    expect(buildEstimateKey({ ...base, seq: 6n })).not.toBe(k) // reseq
    expect(buildEstimateKey({ ...base, hasToolUseSibling: true })).not.toBe(k) // opener arrived
    expect(buildEstimateKey({ ...base, toolUseContentVersion: 1 })).not.toBe(k) // opener edited in place
    expect(buildEstimateKey({ ...base, uiVersion: 1 })).not.toBe(k) // expand/diff toggle
    expect(buildEstimateKey({ ...base, contentVersion: 1 })).not.toBe(k) // in-place body edit
    // Command-stream PRESENCE flip: an empty-body Codex reasoning row classifies
    // assistant_thinking vs hidden on it, so the off-screen estimate must re-key.
    expect(buildEstimateKey({ ...base, hasCommandStream: true })).not.toBe(k)
  })

  it('is stable for identical inputs (no spurious re-estimate)', () => {
    expect(buildEstimateKey(base)).toBe(buildEstimateKey({ ...base }))
  })
})

describe('chatheightestimator buildEstimateEpoch', () => {
  const base = { contentWidth: 800, expandAgentThoughts: true, diffView: 'unified' as const }

  it('pins the pipe-joined format', () => {
    expect(buildEstimateEpoch(base)).toBe('800|1|unified')
    expect(buildEstimateEpoch({ ...base, expandAgentThoughts: false })).toBe('800|0|unified')
  })

  it('changes when any global height input changes', () => {
    const e = buildEstimateEpoch(base)
    expect(buildEstimateEpoch({ ...base, contentWidth: 801 })).not.toBe(e) // wrap width
    expect(buildEstimateEpoch({ ...base, expandAgentThoughts: false })).not.toBe(e) // thinking rows
    expect(buildEstimateEpoch({ ...base, diffView: 'split' })).not.toBe(e) // diff rows
  })
})
