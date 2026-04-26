/* eslint-disable solid/no-innerhtml -- HTML is produced via shiki, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { BashInput, GrepInput } from '~/types/toolMessages'
import { relativizePath } from '~/lib/paths'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { firstNonEmptyLine } from '../../../rendererUtils'
import { renderBashHighlight } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'

/** Derive a summary element for a generic tool_use (Bash command, search paths). */
export function deriveToolSummary(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | undefined {
  switch (toolName) {
    case CLAUDE_TOOL.BASH: {
      const cmd = (input as BashInput).command
      if (!cmd)
        return undefined
      const firstLine = firstNonEmptyLine(cmd) ?? cmd
      return <div class={toolInputSummary} innerHTML={renderBashHighlight(firstLine)} />
    }
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
