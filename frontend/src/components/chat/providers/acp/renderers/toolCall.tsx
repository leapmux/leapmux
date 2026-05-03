import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { ToolUseLayout } from '../../../toolRenderers'
import { kindIcon, kindLabel } from './helpers'

/** Render an ACP tool_call (pending status) — like Claude Code's tool_use header. */
export function acpToolCallRenderer(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const kind = toolUse.kind as string | undefined
  const title = (toolUse.title as string | undefined) || kind || 'Tool'
  const icon = kindIcon(kind)

  return (
    <ToolUseLayout
      icon={icon}
      toolName={kindLabel(kind)}
      title={title}
      context={context}
    />
  )
}
