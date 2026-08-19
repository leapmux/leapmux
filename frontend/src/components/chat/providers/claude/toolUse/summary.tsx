import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { GrepInput, SendMessageInput } from '~/types/toolMessages'
import { relativizePath } from '~/lib/paths'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { toolInputSummary } from '../../../toolStyles.css'

/** The longest message preview the summary line shows before it clips. */
const SEND_MESSAGE_PREVIEW_LIMIT = 120

/** Derive a summary element for a generic tool_use (search paths, etc.). */
export function deriveToolSummary(toolName: string, input: Record<string, unknown>, context?: RenderContext): JSX.Element | undefined {
  switch (toolName) {
    case CLAUDE_TOOL.GREP: {
      const path = (input as GrepInput).path
      if (!path)
        return undefined
      return <div class={toolInputSummary}>{relativizePath(path, context?.workingDir, context?.homeDir)}</div>
    }
    // The card's title carries the RECIPIENT, so the message itself belongs
    // here. Without it the parent steering a subagent is invisible: the user
    // sees who was addressed and never what was said.
    //
    // `message` is a plain string for an ordinary send and an object for the
    // structured kinds (shutdown_request, plan_approval_response), which have no
    // one-line form worth showing. toolInputSummary has no height cap, unlike
    // Bash's collapsed command, so a long steering message is clipped here
    // rather than allowed to inflate the row.
    case CLAUDE_TOOL.SEND_MESSAGE: {
      const { message } = input as SendMessageInput
      if (typeof message !== 'string')
        return undefined
      const line = message.trim().split('\n', 1)[0]
      if (!line)
        return undefined
      const preview = line.length > SEND_MESSAGE_PREVIEW_LIMIT
        ? `${line.slice(0, SEND_MESSAGE_PREVIEW_LIMIT)}\u2026`
        : line
      return <div class={toolInputSummary}>{preview}</div>
    }
    default:
      return undefined
  }
}
