import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_ARTIFACT, PI_BLOCK_TYPE, PI_CONTENT_BLOCK, PI_EVENT, PI_RESULT_FIELD, PI_SUPPLEMENT } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

// Three vocabularies meet in this module, and each object keeps its own.
// `PI_RESULT_FIELD` holds the fields of PI's own frame. `PI_SUPPLEMENT` and
// `PI_ARTIFACT` hold the fields of the envelope LEAPMUX writes beside that frame.
//
// The identity keys spell the same two words on both sides today, so ONE constant read
// both objects and every comparison still passed. A rename on either side then moves
// both reads at once: the read of PI's frame looks for LeapMux's word, finds nothing,
// and the join stops for every row. A recovered output would disappear silently rather
// than fail where somebody sees it.

/**
 * Put a retained call's partial result on its start frame.
 *
 * Pi puts a call's result on its tool_execution_end event and sends none when the
 * turn ends first, so the last tool_execution_update is the only copy. The worker
 * stores the agent's own START frame and keeps that copy beside it.
 *
 * The identity keys are checked first: a supplement that states another call, or
 * another tool, cannot reach this row.
 */
function resolvePiIncompleteTool(
  original: Record<string, unknown>,
  extra: Record<string, unknown>,
): Record<string, unknown> {
  const partial = extra[PI_SUPPLEMENT.PartialResult]
  if (!isObject(partial) || !pickString(original, PI_RESULT_FIELD.ToolCallID) || !pickString(original, PI_RESULT_FIELD.ToolName)
    || extra[PI_SUPPLEMENT.ToolCallID] !== original[PI_RESULT_FIELD.ToolCallID]
    || extra[PI_SUPPLEMENT.ToolName] !== original[PI_RESULT_FIELD.ToolName]) {
    return original
  }
  // Resolving an already-resolved frame must return the SAME object, so a caller
  // that resolves twice does not rebuild the row.
  if (original[PI_RESULT_FIELD.Result] === partial)
    return original
  return { ...original, [PI_RESULT_FIELD.Result]: partial }
}

/** Resolve saved artifacts through the original call identity and artifact paths. */
export function resolvePiMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const extra = parsed.supplementalContent
  // A retained call's row is its START frame, which carries no result, so the two
  // supplement shapes never meet on one row.
  if (original && original.type === PI_EVENT.ToolExecutionStart && isObject(extra))
    return resolvePiIncompleteTool(original, extra)
  if (!original || original.type !== PI_EVENT.ToolExecutionEnd || !isObject(extra)
    || !pickString(original, PI_RESULT_FIELD.ToolCallID) || !pickString(original, PI_RESULT_FIELD.ToolName)
    || extra[PI_SUPPLEMENT.ToolCallID] !== original[PI_RESULT_FIELD.ToolCallID]
    || extra[PI_SUPPLEMENT.ToolName] !== original[PI_RESULT_FIELD.ToolName]) {
    return original
  }
  const result = pickObject(original, PI_RESULT_FIELD.Result)
  const details = pickObject(result, PI_RESULT_FIELD.Details)
  if (!result || !details)
    return original
  let content = result[PI_RESULT_FIELD.Content]
  let mcpResult = details[PI_RESULT_FIELD.McpResult]
  const outputFile = pickObject(extra, PI_SUPPLEMENT.OutputFile)
  const guard = pickObject(details, PI_RESULT_FIELD.OutputGuard)
  if (outputFile && guard?.[PI_RESULT_FIELD.Truncated] === true && pickString(guard, PI_RESULT_FIELD.FullOutputPath)
    && outputFile[PI_ARTIFACT.Path] === guard[PI_RESULT_FIELD.FullOutputPath]
    && typeof outputFile[PI_ARTIFACT.Text] === 'string' && Array.isArray(content) && isObject(content[0])
    && content[0][PI_CONTENT_BLOCK.Type] === PI_BLOCK_TYPE.Text
    && typeof content[0][PI_CONTENT_BLOCK.Text] === 'string'
    && content[0][PI_CONTENT_BLOCK.Text] !== outputFile[PI_ARTIFACT.Text]) {
    content = [{ ...content[0], [PI_CONTENT_BLOCK.Text]: outputFile[PI_ARTIFACT.Text] }, ...content.slice(1)]
  }
  const native = pickObject(details, PI_RESULT_FIELD.McpResult)
  const resultFile = pickObject(extra, PI_SUPPLEMENT.McpResultFile)
  if (resultFile && native?.[PI_RESULT_FIELD.Omitted] === true
    && !Object.hasOwn(native, PI_RESULT_FIELD.Content) && !Object.hasOwn(native, PI_RESULT_FIELD.Contents)
    && pickString(native, PI_RESULT_FIELD.FullResultPath)
    && resultFile[PI_ARTIFACT.Path] === native[PI_RESULT_FIELD.FullResultPath]
    && isObject(resultFile[PI_ARTIFACT.Result])) {
    mcpResult = resultFile[PI_ARTIFACT.Result]
  }
  if (content === result[PI_RESULT_FIELD.Content] && mcpResult === details[PI_RESULT_FIELD.McpResult])
    return original
  // Computed keys, the same generated constants every read above uses. A frame built
  // from raw literals would carry the OLD key beside the agent's new one after a
  // rename, and the join would then do nothing without a word about it.
  return {
    ...original,
    [PI_RESULT_FIELD.Result]: {
      ...result,
      ...(content !== result[PI_RESULT_FIELD.Content] ? { [PI_RESULT_FIELD.Content]: content } : {}),
      ...(mcpResult !== details[PI_RESULT_FIELD.McpResult]
        ? { [PI_RESULT_FIELD.Details]: { ...details, [PI_RESULT_FIELD.McpResult]: mcpResult } }
        : {}),
    },
  }
}
