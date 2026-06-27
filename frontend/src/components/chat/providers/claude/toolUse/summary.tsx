import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { GrepInput } from '~/types/toolMessages'
import { relativizePath } from '~/lib/paths'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { toolInputSummary } from '../../../toolStyles.css'

/** Derive a summary element for a generic tool_use (search paths, etc.). */
export function deriveToolSummary(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | undefined {
  switch (toolName) {
    case CLAUDE_TOOL.GREP: {
      const path = (input as GrepInput).path
      if (!path)
        return undefined
      return <div class={toolInputSummary}>{relativizePath(path, context?.workingDir, context?.homeDir)}</div>
    }
    default:
      return undefined
  }
}
