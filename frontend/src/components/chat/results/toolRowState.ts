import type { Accessor } from 'solid-js'
import type { MessageUiKey } from '../messageUiKeys'
import type { ToolKind } from '../model/toolKind'
import type { MessageUiState, ToolResultRenderContext } from '../renderContext'
import type { ToolCallDispatch } from './tools/index'
import { createMemo } from 'solid-js'
import { MESSAGE_UI_KEY } from '../messageUiKeys'

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
 * The render context that the bodies of one tool row read: the row's own context,
 * with TOOL_RESULT_EXPANDED bound to the row's expand state.
 *
 * Local state supports an isolated render without a message-store host. The expansion
 * reader and writer must use the same state: replacing only the reader leaves the
 * writer attached to another signal, and the view then ignores each expansion change.
 * So this context supplies both operations for the row's expansion key, and forwards
 * every other key to the message host. Call it within the row's reactive owner.
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
