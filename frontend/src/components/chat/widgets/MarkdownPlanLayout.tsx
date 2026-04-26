/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from '../markdownEditor/markdownContent.css'
import { ToolUseLayout } from '../toolRenderers'

export interface MarkdownPlanLayoutProps {
  /** Tool name shown in the header chip (e.g. "Plan", "ExitPlanMode"). */
  toolName: string
  /** Visible title (e.g. "Proposed Plan", "Leaving Plan Mode"). */
  title: string
  /** Markdown body of the plan. Empty string suppresses the body and copy/reply actions. */
  planText: string
  context?: RenderContext
}

/**
 * Bubble-less ToolUseLayout that renders a plan as a markdown body with
 * Copy + Reply header actions. Shared between Codex `plan` items and Claude
 * `ExitPlanMode` tool_use blocks — they differ only in title/toolName/source.
 */
export function MarkdownPlanLayout(props: MarkdownPlanLayoutProps): JSX.Element {
  const { copied, copy } = useCopyButton(() => props.planText || undefined)
  const handleReply = () => props.context?.onReply?.(props.planText)

  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName={props.toolName}
      title={props.title}
      alwaysVisible={true}
      bordered={false}
      context={props.context}
      headerActions={{
        onReply: props.planText && props.context?.onReply ? handleReply : undefined,
        onCopyMarkdown: props.planText ? copy : undefined,
        markdownCopied: copied(),
      }}
    >
      <Show when={props.planText}>
        <hr />
        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(props.planText)} />
      </Show>
    </ToolUseLayout>
  )
}
