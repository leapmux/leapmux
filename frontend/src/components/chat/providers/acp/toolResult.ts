import type { MessageCategory } from '../../messageClassification'
import type { ToolMessageInput, ToolResultMeta } from '../registry'
import type { ACPToolAdapter } from './toolPresentation'
import { toolPresentationMeta } from '../../results/toolResultMeta'
import { acpToolFinished, acpToolPresentation, parsedACPToolCall, resolveACPToolCall } from './toolPresentation'

export function acpToolResultMeta(
  category: MessageCategory,
  input: ToolMessageInput,
  adapter?: ACPToolAdapter,
): ToolResultMeta | null {
  if (category.kind !== 'tool_use')
    return null
  const tool = parsedACPToolCall(input.parsed.parentObject)
  if (!tool || !acpToolFinished(tool, input.parsed.completion))
    return null
  const presentation = acpToolPresentation(resolveACPToolCall(tool, input.request?.parentObject), adapter, input.parsed.supplementalContent, input.parsed.completion)
  return toolPresentationMeta(presentation)
}
