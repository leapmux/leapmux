import type { JSX } from 'solid-js'
import type { PlanRenderContext } from '../renderContext'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { createMemo, Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { markdownContent } from '../markdownEditor/markdownContent.css'
import { renderMarkdownForContext } from '../markdownRendering'
import { ToolUseLayout } from './ToolUseLayout'

export interface MarkdownPlanLayoutProps {
  /** Tool name shown in the header chip (e.g. "Plan", "ExitPlanMode"). */
  toolName: string
  /** Visible title (e.g. "Proposed Plan", "Leaving Plan Mode"). */
  title: string
  /** Markdown body of the plan. Empty string suppresses the body and copy/reply actions. */
  planText: string
  context?: PlanRenderContext
}

/**
 * Bubble-less ToolUseLayout that renders a plan as a markdown body with
 * Copy + Reply header actions. Shared between Codex `plan` items and Claude
 * `ExitPlanMode` tool_use blocks — they differ only in title/toolName/source.
 */
export function MarkdownPlanLayout(props: MarkdownPlanLayoutProps): JSX.Element {
  const { copied, copy } = useCopyButton(() => props.context?.premeasureMode ? undefined : props.planText || undefined)
  const handleReply = () => {
    if (!props.context?.premeasureMode)
      props.context?.onReply?.(props.planText)
  }
  const renderedPlan = createMemo(() => renderMarkdownForContext(props.planText, props.context))

  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName={props.toolName}
      title={props.title}
      alwaysVisible={true}
      bordered={false}
      {...(props.context === undefined ? {} : { context: props.context })}
      headerActions={{
        // Set only when the plan draws them: an explicit `undefined` is not
        // assignable to an optional prop, and the actions row reads absent
        // the same.
        ...(props.planText && props.context?.onReply ? { onReply: handleReply } : {}),
        ...(props.planText ? { onCopyMarkdown: copy } : {}),
        markdownCopied: copied(),
      }}
    >
      <Show when={props.planText}>
        <hr />
        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} ref={cachedInnerHtml(renderedPlan)} />
      </Show>
    </ToolUseLayout>
  )
}
