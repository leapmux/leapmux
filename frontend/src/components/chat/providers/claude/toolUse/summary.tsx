import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { GrepInput, SendMessageInput } from '~/types/toolMessages'
import { clipFirstLine } from '~/lib/clipFirstLine'
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
      const { message, summary } = input as SendMessageInput
      // `summary` first. The tool's own schema describes it as "a 5-10 word
      // summary shown as a one-line preview in the UI", and the model writes it
      // for exactly this surface -- so preferring the raw first line of the
      // message threw away the label written for the slot. It is also the only
      // one-line form the STRUCTURED kinds have: for an object-valued `message`
      // the first-line clip yields nothing, and the card showed the recipient
      // with no preview at all.
      const preview = clipFirstLine(
        summary ?? (typeof message === 'string' ? message : ''),
        SEND_MESSAGE_PREVIEW_LIMIT,
      )
      if (!preview)
        return undefined
      return <div class={toolInputSummary}>{preview}</div>
    }
    default:
      return undefined
  }
}
