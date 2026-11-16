import type { Accessor } from 'solid-js'
import type { MessageUiKey } from '../messageUiKeys'
import type { ToolCallRow } from '../model/row'
import type { ToolKind } from '../model/toolKind'
import type { MessageUiState, ToolResultRenderContext } from '../renderContext'
import type { ToolCallDispatch } from './tools/index'
import type { ToolRowView } from './tools/renderer'
import { createMemo, untrack } from 'solid-js'
import { useSharedExpandedState } from '../messageRenderers'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolRowPosition } from '../model/derivations'
import { dispatchParts, TOOL_KIND_RENDERERS } from './tools/index'

/**
 * The expand key that one mounted row of a call reads: AGENT_PROMPT for a row that
 * draws no result and whose kind words a key of its own, TOOL_RESULT_EXPANDED
 * otherwise. The request row and the pending row of one call draw no result, so both
 * read the same key, and one Expand opens both.
 */
export function rowExpandKey(drawsResult: boolean, parts: ToolCallDispatch<ToolKind>): MessageUiKey {
  return (!drawsResult && parts.renderer.requestExpandUiKey) || MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
}

/**
 * Every key that a row which draws no result can read (see rowExpandKey):
 * TOOL_RESULT_EXPANDED and the key of each kind that words one of its own. A reader that
 * needs to know whether such a row is expanded, without its kind, tests these.
 */
export const REQUEST_ROW_EXPAND_KEYS: readonly MessageUiKey[] = [
  ...new Set<MessageUiKey>([
    MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED,
    ...Object.values(TOOL_KIND_RENDERERS).flatMap(renderer => renderer.requestExpandUiKey ?? []),
  ]),
]

/**
 * The render context that the bodies of one tool row read: the row's own context,
 * with TOOL_RESULT_EXPANDED bound to the row's expand state.
 *
 * Local state supports an isolated render without a message-store host. The expansion
 * reader and writer must use the same state: replacing only the reader leaves the
 * writer attached to another signal, and the view then ignores each expansion change.
 * So this context supplies both operations for TOOL_RESULT_EXPANDED, bound to the row's
 * expand state whichever key backs that state (see rowExpandKey), and forwards every
 * other key to the message host. Call it within the row's reactive owner.
 */
export function createRowBodyContext(
  context: () => ToolResultRenderContext | undefined,
  expanded: Accessor<boolean>,
  setExpanded: (value: boolean) => void,
): Accessor<ToolResultRenderContext> {
  const rowUiState: MessageUiState = {
    get: key => key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
      ? expanded()
      : context()?.getMessageUiState?.(key),
    set: (key, value) => {
      if (key === MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
        setExpanded(value)
      else
        context()?.setMessageUiState?.(key, value)
    },
  }
  return createMemo<ToolResultRenderContext>(() => {
    const body: ToolResultRenderContext = Object.create(context() ?? null)
    Object.defineProperties(body, {
      getMessageUiState: { value: rowUiState.get },
      setMessageUiState: { value: rowUiState.set },
    })
    return body
  })
}

/** The state that one mounted tool row builds before it draws (see createToolRowState). */
export interface ToolRowState {
  /** The dispatch of the row's own call: each hook with the call that it reads. */
  parts: Accessor<ToolCallDispatch<ToolKind>>
  expanded: Accessor<boolean>
  setExpanded: (value: boolean | ((previous: boolean) => boolean)) => void
  /** The context that the bodies of the row read (see createRowBodyContext). */
  bodyContext: Accessor<ToolResultRenderContext>
  /** The view that each hook of the row draws with. */
  view: Accessor<ToolRowView>
}

/**
 * Build the state of one mounted tool row: its dispatch, the expand state that it shares
 * with the other rows of its call (see rowExpandKey), the context that its bodies read,
 * and its view. The result row, the request row and the pending row of a call each build
 * it the same way. Call it within the row's reactive owner.
 */
export function createToolRowState(options: {
  row: () => ToolCallRow
  context: () => ToolResultRenderContext | undefined
  /** Whether the row draws the result. Read once: a row keeps the key that it mounted with. */
  drawsResult: () => boolean
  onSummaryOverflow: (overflows: boolean) => void
}): ToolRowState {
  // Dispatch supplies the correlated renderer and call to each hook, so no reader needs an
  // assertion to pair them again.
  const parts = createMemo((): ToolCallDispatch<ToolKind> => dispatchParts(options.row().call))
  const expandKey = untrack(() => rowExpandKey(options.drawsResult(), parts()))
  const [expanded, setExpanded] = useSharedExpandedState(options.context, expandKey)
  const bodyContext = createRowBodyContext(options.context, expanded, value => setExpanded(value))
  // Solid compiles `view={view()}` to a getter. A plain function allocates a new view for
  // each child property read, and the new identity keeps the children from reusing the
  // previous value. A memo keeps the view until its inputs change.
  const view = createMemo((): ToolRowView => ({
    // The complete row position and its role constraints. A field-by-field
    // reconstruction can admit an invalid sibling.
    ...toolRowPosition(options.row()),
    context: bodyContext(),
    expanded,
    setExpanded: value => setExpanded(value),
    // The result body draws first, so its images start at index zero. imagesForRow uses
    // the same order, and an image tab uses that list's index.
    imageIndexOffset: 0,
    onSummaryOverflow: options.onSummaryOverflow,
  }))
  return {
    parts,
    expanded,
    setExpanded,
    bodyContext,
    view,
  }
}
