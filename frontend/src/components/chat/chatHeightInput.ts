import type { HeightInput, RowUiState } from './chatHeightEstimator'
import type { BodyTextMetrics } from './chatHeightShared'
import type { MessageCategory } from './messageClassification'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { asContentArray, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { TOOL_USE_KIND_PREFIX, toolNameFromKind } from './chatHeightEstimator'
import { bodyTextMetrics, countLines, isToolResultError, messageContentBlocks, rowCarriesDiff, toLineLengths, toolResultBlockText } from './chatHeightShared'
import { uiFlagsConsumedBy } from './messageUiKeys'
import { notificationThreadMetrics } from './notificationRenderers'
import { pluginFor } from './providers/registry'
import { firstNonEmptyLine } from './rendererUtils'

/**
 * Feature-extraction layer for the chat height estimator -- the orchestrator
 * that turns an already-classified, already-parsed message + its UI state into
 * the pure `HeightInput` the estimator (`chatHeightEstimator.ts`) consumes.
 *
 * It owns the PROVIDER-NEUTRAL slice: text/line counts, attachments, images,
 * collapsed/error flags, the raw-JSON line count, and the tool-input summaries
 * (Bash/Grep summary, TodoWrite/ExitPlanMode/AskUserQuestion bodies) read from
 * the classified `category.toolUse.input`. The PROVIDER-SPECIFIC slice -- diff
 * geometry, the tool_result `bodyMarkdown`/`hasHeader` flags, and the
 * `result_divider` detail, all of which depend on per-provider wire shapes --
 * is delegated to each provider's `Provider.heightMetrics` hook and merged on
 * top (see `buildHeightInput`). The estimator that turns a `HeightInput` into
 * pixels stays a pure function; this module is the impure half but no longer
 * sniffs any one provider's wire format itself.
 */

interface BuildHeightInputArgs {
  kind: string
  toolName?: string
  hasSpanLines: boolean
  category: MessageCategory
  parsed: ParsedMessageContent
  state: RowUiState
  /** The row's provider, used to dispatch its `heightMetrics` hook. */
  agentProvider?: AgentProvider
  /**
   * The paired tool_use sibling (resolved by spanId for a tool_result row, same
   * as the renderer's lookup), so a provider hook can reach the input-side data
   * its renderer uses (Claude's edit input, Pi's start args).
   */
  toolUseParsed?: ParsedMessageContent
  /**
   * The agent's working dir / home dir, used to relativize a tool path one-line
   * summary (Grep) so the estimate matches the renderer's relativized text rather
   * than a longer absolute path. Absent -> the path is sized as-is.
   */
  workingDir?: string
  homeDir?: string
}

/**
 * Assign a body's text metrics onto a HeightInput in ONE place, so a future
 * change to which fields a text body contributes (e.g. a new wrap input) lands
 * here instead of being silently forgotten at one of the call sites. The mono
 * tool paths (Bash-expanded, the one-line summary) deliberately set their own
 * narrower fields and do NOT route through this.
 */
function setTextBody(input: HeightInput, m: BodyTextMetrics): void {
  input.textLength = m.textLength
  input.logicalLineCount = m.logicalLineCount
  input.lineLengths = m.lineLengths
}

/**
 * Extract the displayed text body (length + line count + per-line lengths) from a
 * parsed message.
 *
 * Reads only the PERSISTED message content -- never the live command stream. A
 * streaming reasoning/tool row's rendered body grows on every delta, so measuring
 * it analytically would mean re-running this parse (+ wrap + diff geometry) on each
 * chunk, which is far too costly for a row that is continuously changing. Streaming
 * rows instead sit at the measured tail (the reader is following) and MEASURE; the
 * analytical estimate is the pre-mount/off-screen fallback for the persisted body,
 * and a streaming row that scrolls off re-pins from its last real measurement.
 */
function extractText(parsed: ParsedMessageContent): BodyTextMetrics {
  const blocks = messageContentBlocks(parsed)
  let text = ''
  if (blocks) {
    const parts: string[] = []
    // Top-level text AND thinking blocks (assistant/user prose, agent reasoning).
    // Thinking content lives in a `type:"thinking"` block's `thinking` field, not
    // a `text` field, so it must be named here or every thinking row extracts as
    // empty and is estimated as a bare 82px bubble.
    const top = joinContentParagraphs(blocks, { text: 'text', thinking: 'thinking' })
    if (top)
      parts.push(top)
    // Text nested inside tool_result blocks (bash/read/grep output) -- this is
    // the dominant body height for an expanded tool result. toolResultBlockText is
    // the shared per-block join (chatHeightShared), so the Claude hook's
    // resultContentText can't drift from what this sizes.
    for (const b of blocks)
      parts.push(toolResultBlockText(b))
    text = parts.filter(Boolean).join('\n')
  }
  if (!text) {
    const pc = parsed.parentObject?.content
    if (typeof pc === 'string')
      text = pc
  }
  if (!text) {
    const tl = parsed.topLevel
    if (isObject(tl) && typeof tl.text === 'string')
      text = tl.text
  }
  return bodyTextMetrics(text, 'markdown')
}

/** Count MCP image blocks in a tool result's content array. */
function countImages(parsed: ParsedMessageContent): number {
  // Reads the SAME content source extractText does (messageContentBlocks): a
  // tool_result whose envelope parsed with parentObject undefined but topLevel
  // carrying the content blocks would otherwise have its text sized (extractText
  // falls back to topLevel) while its images are dropped here, under-estimating the
  // row by ~one image height per block.
  const blocks = messageContentBlocks(parsed)
  if (!blocks)
    return 0
  let n = 0
  for (const b of blocks) {
    if (isObject(b) && b.type === 'tool_result' && Array.isArray(b.content)) {
      for (const inner of asContentArray(b.content) ?? []) {
        if (isObject(inner) && inner.type === 'image')
          n++
      }
    }
    else if (isObject(b) && b.type === 'image') {
      n++
    }
  }
  return n
}

/**
 * Map a classified, parsed message + its UI state to a pure HeightInput.
 *
 * Builds the provider-neutral slice (`buildGenericInput`), then merges the
 * row's provider `heightMetrics` hook on top:
 *  - DIFF PRECEDENCE: when the hook reports a diff, the row is sized as a diff
 *    (the generic slice is skipped), matching the estimator's first check.
 *  - Otherwise the hook's `bodyMarkdown` / `result_divider` detail override the
 *    generic fields, and `hasHeader` is the generic `isError` OR'd with the
 *    provider's agent/task/interrupted reasons.
 */
export function buildHeightInput(args: BuildHeightInputArgs): HeightInput {
  const { kind, toolName, hasSpanLines, category, parsed, state, agentProvider, toolUseParsed } = args
  const base: HeightInput = { kind, toolName, hasSpanLines }

  const hook = pluginFor(agentProvider)?.heightMetrics?.(category, parsed, toolUseParsed, state) ?? {}

  // A row that carries a diff renders the diff block regardless of which side
  // (a Codex/ACP tool_use or a Claude/Pi tool_result), so size it as a diff.
  // `diffView` stays generic -- it is interactive UI state, not provider data.
  if (rowCarriesDiff(hook))
    return { ...base, ...hook, diffView: state.diffView }

  const generic = buildGenericInput(args, base)
  const merged: HeightInput = { ...generic, ...hook }
  // isError is the generic (universal tool_result block) signal; the provider
  // hook contributes the agent/task/interrupted header reasons. Either forces a
  // leading ToolStatusHeader.
  if (generic.isError || hook.hasHeader)
    merged.hasHeader = true
  return merged
}

/**
 * Build the provider-neutral slice of a row's HeightInput. Takes the whole
 * `BuildHeightInputArgs` (rather than re-spreading its fields into a long
 * positional list) so adding a pre-mount input is a one-field change here, and a
 * transposed `kind`/`toolName` is impossible.
 */
function buildGenericInput(args: BuildHeightInputArgs, base: HeightInput): HeightInput {
  const { kind, toolName, category, parsed, state, agentProvider } = args
  const input: HeightInput = { ...base }

  if (kind === 'tool_result') {
    // Extract the body text even when images are present: an MCP result can carry
    // both a text body and an image, and the image branch sizes them together.
    // Always carries the per-line lengths: a markdown body (the provider hook's
    // `bodyMarkdown`) needs them for the block-gap model; the mono path ignores
    // them (estimateToolResult sizes mono from textLength/logicalLineCount).
    setTextBody(input, extractText(parsed))
    const images = countImages(parsed)
    if (images > 0)
      input.imageCount = images
    input.collapsed = state.collapsed
    // A status header renders only when the result errored (here, the universal
    // signal), was interrupted, or is a sub-agent/task result; the latter two
    // are provider-specific and folded in via the hook's `hasHeader`.
    input.isError = isToolResultError(parsed)
    return input
  }

  if (kind === 'hidden' || kind === 'unsupported_provider') {
    // Shown raw-JSON card: line count of the pretty-printed envelope.
    input.jsonLineCount = countLines(parsed.rawText)
    return input
  }

  // Header-only tool_use (Bash/Read/Grep/...): no diff, no result body. Its only
  // extra height is the one-line summary shown under the header (the Bash
  // command's first line), which lives in the tool input -- not a text block --
  // so it would otherwise extract as empty and estimate as a bare header line.
  if (kind.startsWith(TOOL_USE_KIND_PREFIX)) {
    const tn = toolName ?? toolNameFromKind(kind) ?? ''
    // TodoWrite / ExitPlanMode render an alwaysVisible body (a checklist / a
    // markdown plan) under the header, sized by their own estimators.
    if (tn === 'TodoWrite') {
      input.todoCount = countTodos(category)
      return input
    }
    if (tn === 'ExitPlanMode') {
      setTextBody(input, bodyTextMetrics(stringInput(category, 'plan'), 'markdown'))
      return input
    }
    if (tn === 'AskUserQuestion') {
      const ask = countAskOptions(category)
      input.askQuestionCount = ask.questions
      input.askOptionCount = ask.options
      return input
    }
    // An EXPANDED multi-line Bash command renders the full command body, not the
    // one-line summary (state.toolBodyExpanded resolves MESSAGE_UI_KEY.TOOL_USE_LAYOUT).
    // Which tools consume toolBodyExpanded lives in uiFlagsConsumedBy, shared with
    // ChatView's rowUiState, so the estimate can't read a flag the resolver omits.
    if (uiFlagsConsumedBy(kind, tn).toolBodyExpanded && state.toolBodyExpanded) {
      const command = stringInput(category, 'command')
      if (command.includes('\n')) {
        input.textLength = command.length
        input.logicalLineCount = countLines(command)
        // Per-hard-line lengths so estimateToolUseHeader sums each line's wrap (the
        // pre-wrap summary block wraps each line independently); without these it
        // falls back to the flat total-wrap model and under-counts a several-long-
        // line command.
        input.lineLengths = toLineLengths(command)
        return input
      }
    }
    const summary = extractToolSummary(tn, category, args.workingDir, args.homeDir)
    if (summary) {
      input.textLength = summary.length
      input.logicalLineCount = 1 // only the first non-empty line is rendered
    }
    return input
  }

  setTextBody(input, extractText(parsed))
  // Which kinds render an expandable bubble lives in uiFlagsConsumedBy (shared with
  // ChatView's rowUiState), so a new expandable kind is declared once.
  if (uiFlagsConsumedBy(kind).expanded)
    input.expanded = state.expanded
  if (kind === 'user_content')
    input.attachmentCount = countAttachments(parsed)
  if (category.kind === 'notification' && Array.isArray(category.messages)) {
    input.childCount = category.messages.length
    // Size from the COALESCED body the renderer actually emits, NOT extractText: a
    // notification's per-child text is DERIVED per provider (threadEntriesFor), so it
    // never reaches extractText's content-block read. notificationThreadMetrics is the
    // renderer's own coalescing, so the estimate sizes from exactly what mounts.
    const metrics = notificationThreadMetrics(category.messages, agentProvider)
    input.textLength = metrics.textLength
    input.logicalLineCount = metrics.blockCount
  }
  return input
}

/**
 * The tool_use `input` object for a classified message, narrowed to an object
 * (or undefined when the message isn't a tool_use or carries no object input).
 * `category.toolUse` is the whole `{type,name,input}` block, so the input lives
 * at `toolUse.input` -- one level deeper than it looks (matching
 * `toolUse/index.tsx`'s `pickObject(toolUse, 'input')`). The tool-input readers
 * below share this unwrap.
 */
function toolUseInput(category: MessageCategory): Record<string, unknown> | undefined {
  if (category.kind !== 'tool_use')
    return undefined
  const input = isObject(category.toolUse) ? category.toolUse.input : undefined
  return isObject(input) ? input : undefined
}

/**
 * First-line summary text shown under a header-only tool_use. Bash renders the
 * command's first non-empty line as a monospace summary; other tools either
 * carry their info in the header title or render no summary body tall enough to
 * matter (a short path/query stays within the WARN threshold).
 */
function extractToolSummary(toolName: string | undefined, category: MessageCategory, workingDir?: string, homeDir?: string): string {
  const input = toolUseInput(category)
  if (!input)
    return ''
  if (toolName === 'Bash' && typeof input.command === 'string')
    // Mirror deriveToolSummary's `firstNonEmptyLine(cmd) ?? cmd`: a whitespace-only
    // command has no first non-empty line, and the renderer falls back to the whole
    // command -- so the estimate must too, or it sizes a bare header where a body mounts.
    return firstNonEmptyLine(input.command) ?? input.command
  // Grep renders its path RELATIVIZED to the agent's working/home dir
  // (deriveToolSummary -> relativizePath in providers/claude/toolUse/summary.tsx), so
  // size the relativized form. Sizing the raw absolute path would over-estimate a
  // deeply-nested path's one-line summary and leave the off-screen row too tall until
  // it mounts. relativizePath returns the path unchanged when workingDir is absent.
  if (toolName === 'Grep' && typeof input.path === 'string')
    return relativizePath(input.path, workingDir, homeDir)
  return ''
}

/** Count attachment chips in a user_content message. */
function countAttachments(parsed: ParsedMessageContent): number {
  const attachments = parsed.parentObject?.attachments
  return Array.isArray(attachments) ? attachments.length : 0
}

/** TodoWrite checklist row count, read from the tool_use `input.todos` array. */
function countTodos(category: MessageCategory): number {
  const todos = toolUseInput(category)?.todos
  return Array.isArray(todos) ? todos.length : 0
}

/**
 * A string-typed tool_use input field (ExitPlanMode `input.plan`, Bash
 * `input.command`), or '' when absent or non-string.
 */
function stringInput(category: MessageCategory, field: 'plan' | 'command'): string {
  const value = toolUseInput(category)?.[field]
  return typeof value === 'string' ? value : ''
}

/**
 * AskUserQuestion question + total-option counts, from `input.questions[]` (each
 * question carrying an `options[]` array). See askUserQuestion.tsx.
 */
function countAskOptions(category: MessageCategory): { questions: number, options: number } {
  const rawQuestions = toolUseInput(category)?.questions
  const questions = Array.isArray(rawQuestions) ? rawQuestions : []
  let options = 0
  for (const q of questions) {
    if (isObject(q) && Array.isArray(q.options))
      options += q.options.length
  }
  return { questions: questions.length, options }
}
