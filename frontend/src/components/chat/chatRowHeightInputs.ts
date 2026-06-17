import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { EstimateBreakdown, HeightCtx, HeightInput, RowUiState } from './chatHeightEstimator'
import type { MessageUiKey } from './messageUiKeys'
import type { VirtualItem } from './useChatVirtualizer'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { toJson } from '@bufbuild/protobuf'
import { AgentChatMessageSchema } from '~/generated/leapmux/v1/agent_pb'
import { createLogger } from '~/lib/logger'
import { estimateRowHeight, TOOL_USE_KIND_PREFIX, warnIfHeightMismatch } from './chatHeightEstimator'
import { buildHeightInput } from './chatHeightInput'
import { resolveRowUiState } from './chatMessageUiState'

// ---------------------------------------------------------------------------
// Row height-input derivation
//
// The ClassifiedEntry -> HeightInput -> analytical estimate pipeline, extracted from
// ChatView so the estimate plumbing it merely hosts lives in one coupling-free unit
// (mirroring the classified-entry cache / scroll-hook / virtualizer extractions). The
// host stays the owner of the reactive state this reads (the prefs signals, the
// measured width, the entry cache); they are threaded in as the deps below.
// ---------------------------------------------------------------------------

export interface RowHeightInputsDeps {
  /** The cached classified entry for a message id (for the height-estimate-miss logger). */
  getEntry: (id: string) => ClassifiedEntry | undefined
  /** Per-message boolean UI flag reader (expand/collapse toggles). */
  getMessageUiBool: (messageId: string, key: MessageUiKey) => boolean | undefined
  /** Per-message diff-view override reader. */
  getLocalDiffView: (messageId: string) => 'unified' | 'split' | undefined
  /**
   * The global "expand agent thoughts" pref, as an ACCESSOR. Read INSIDE buildRowInput
   * (which the virtualizer runs in its per-row estimate thunk), NOT captured here, so
   * an unmeasured thinking row's resolved state tracks the current global pref.
   */
  expandAgentThoughts: () => boolean
  /** The global diff-view pref, as an ACCESSOR, for the same reactive-in-thunk reason. */
  diffView: () => DiffViewPreference
  /** The paired tool_use opener parse for a spanId (a tool_result sizes its diff from it). */
  getToolUseParsedBySpanId?: (spanId: string) => ParsedMessageContent | undefined
  /**
   * The agent's working/home dir as ACCESSORs (read in-thunk, not captured), used to
   * relativize a Grep path one-line summary so its estimate matches the renderer's
   * relativized text rather than a longer absolute path.
   */
  workingDir: () => string | undefined
  homeDir: () => string | undefined
  /** The shared estimate context (only the measured content width varies at runtime). */
  heightCtx: Accessor<HeightCtx>
}

export interface RowHeightInputs {
  /** The pre-mount analytical height input for an entry (kind + content metrics + UI state). */
  buildRowInput: (entry: ClassifiedEntry) => HeightInput
  /**
   * The analytical estimate for a virtual item: the full breakdown (kind/total/
   * terms/metrics). `.total` is the estimated height the offset map uses; the rest
   * feeds the divergence WARN and the raw-JSON debug surface. Null when the item has
   * no features (the caller seeds the default/running-mean fallback instead).
   */
  estimateItemBreakdown: (item: VirtualItem) => EstimateBreakdown | null
  /** On a row's first measurement, WARN when the analytical estimate diverged notably. */
  logHeightEstimateMiss: (id: string, actual: number) => void
}

export function createRowHeightInputs(deps: RowHeightInputsDeps): RowHeightInputs {
  const heightLog = createLogger('chatHeightEstimator')

  // entryKind: `tool_use:${toolName}` for tools (so the estimator/logger can tell a
  // tall Edit/Write diff from a short Read), the bare category kind otherwise.
  const entryKind = (e: ClassifiedEntry): string =>
    e.category.kind === 'tool_use' ? `${TOOL_USE_KIND_PREFIX}${e.category.toolName}` : e.category.kind

  // The interactive UI state the height estimator needs for a row, resolved PRE-mount
  // by resolveRowUiState. The prefs accessors are invoked HERE -- inside the per-row
  // thunk the virtualizer runs in its estimate memo -- so the resolved values track
  // the current global prefs; per-row toggles re-estimate via the estimateKey.
  const rowUiState = (entry: ClassifiedEntry): RowUiState =>
    resolveRowUiState(entry, {
      getMessageUiBool: deps.getMessageUiBool,
      getLocalDiffView: deps.getLocalDiffView,
      expandAgentThoughts: deps.expandAgentThoughts(),
      diffView: deps.diffView(),
    })

  const buildRowInput = (entry: ClassifiedEntry): HeightInput =>
    buildHeightInput({
      kind: entryKind(entry),
      toolName: entry.category.kind === 'tool_use' ? entry.category.toolName : undefined,
      hasSpanLines: entry.parsedSpanLines.length > 0,
      category: entry.category,
      parsed: entry.parsed,
      state: rowUiState(entry),
      agentProvider: entry.msg.agentProvider,
      // The paired tool_use sibling lets a provider's heightMetrics hook size a
      // tool_result diff exactly as it renders (Claude's edit input, Pi's start
      // args). Resolved by spanId, mirroring MessageBubble's toolUseParsed memo;
      // only tool_result rows have a sibling to look up.
      toolUseParsed: entry.category.kind === 'tool_result'
        ? deps.getToolUseParsedBySpanId?.(entry.msg.spanId)
        : undefined,
      // Read the dir accessors in-thunk (like the prefs above) so a Grep path summary
      // sizes against the same relativized text the renderer shows.
      workingDir: deps.workingDir(),
      homeDir: deps.homeDir(),
    })

  // The analytical estimate for a row: the FULL breakdown. The virtualizer reads
  // `.total` for the offset map and passes the whole object to the raw-JSON debug
  // surface (the same kind/total/terms/metrics the divergence WARN logs). estimateRowHeight
  // builds the breakdown regardless, so returning it instead of just the total is free.
  // Null for a row with no features (the caller seeds DEFAULT_ESTIMATE_PX instead).
  const estimateItemBreakdown = (item: VirtualItem): EstimateBreakdown | null =>
    item.features ? estimateRowHeight(item.features(), deps.heightCtx()) : null

  // On a row's FIRST measurement, compare the analytical estimate against the real
  // height and WARN (raw message JSON + state + estimate breakdown vs actual) when
  // they diverge notably. Wrapped so a serialization failure or a missing entry can
  // never crash a render.
  const logHeightEstimateMiss = (id: string, actual: number) => {
    try {
      const entry = deps.getEntry(id)
      if (!entry)
        return
      const state = rowUiState(entry)
      const breakdown = estimateRowHeight(buildRowInput(entry), deps.heightCtx())
      warnIfHeightMismatch(heightLog, id, breakdown, actual, {
        // Log both as JSON values (not strings) so devtools renders them as
        // expandable objects. `topLevel` is the already-parsed payload; fall
        // back to the raw string only when the content failed to parse.
        state,
        rawMessage: toJson(AgentChatMessageSchema, entry.msg),
        content: entry.parsed.topLevel ?? entry.parsed.rawText,
      })
    }
    catch (err) {
      heightLog.warn('height estimate logging failed', { id, err })
    }
  }

  return { buildRowInput, estimateItemBreakdown, logHeightEstimateMiss }
}
