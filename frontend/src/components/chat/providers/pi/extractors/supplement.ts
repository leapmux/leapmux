import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Put a retained call's partial result on its start frame.
 *
 * Pi puts a call's result on its tool_execution_end event and sends none when the
 * turn ends first, so the last tool_execution_update is the only copy. The worker
 * stores the agent's own START frame and keeps that copy beside it.
 *
 * The identity keys are checked first: a supplement that names another call, or
 * another tool, cannot reach this row.
 */
function resolvePiIncompleteTool(
  original: Record<string, unknown>,
  extra: Record<string, unknown>,
): Record<string, unknown> {
  const partial = extra.partialResult
  if (!isObject(partial) || !pickString(original, 'toolCallId') || !pickString(original, 'toolName')
    || extra.toolCallId !== original.toolCallId || extra.toolName !== original.toolName) {
    return original
  }
  // Resolving an already-resolved frame must return the SAME object, so a caller
  // that resolves twice does not rebuild the row.
  if (original.result === partial)
    return original
  return { ...original, result: partial }
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
    || !pickString(original, 'toolCallId') || !pickString(original, 'toolName')
    || extra.toolCallId !== original.toolCallId || extra.toolName !== original.toolName) {
    return original
  }
  const result = pickObject(original, 'result')
  const details = pickObject(result, 'details')
  if (!result || !details)
    return original
  let content = result.content
  let mcpResult = details.mcpResult
  const outputFile = pickObject(extra, 'outputFile')
  const guard = pickObject(details, 'outputGuard')
  if (outputFile && guard?.truncated === true && pickString(guard, 'fullOutputPath') && outputFile.path === guard.fullOutputPath
    && typeof outputFile.text === 'string' && Array.isArray(content) && isObject(content[0])
    && content[0].type === 'text' && typeof content[0].text === 'string' && content[0].text !== outputFile.text) {
    content = [{ ...content[0], text: outputFile.text }, ...content.slice(1)]
  }
  const native = pickObject(details, 'mcpResult')
  const resultFile = pickObject(extra, 'mcpResultFile')
  if (resultFile && native?.omitted === true && !Object.hasOwn(native, 'content') && !Object.hasOwn(native, 'contents')
    && pickString(native, 'fullResultPath') && resultFile.path === native.fullResultPath && isObject(resultFile.result)) {
    mcpResult = resultFile.result
  }
  if (content === result.content && mcpResult === details.mcpResult)
    return original
  return {
    ...original,
    result: {
      ...result,
      ...(content !== result.content ? { content } : {}),
      ...(mcpResult !== details.mcpResult ? { details: { ...details, mcpResult } } : {}),
    },
  }
}
