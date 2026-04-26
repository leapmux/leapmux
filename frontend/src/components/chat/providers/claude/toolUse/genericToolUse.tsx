/* eslint-disable solid/no-innerhtml -- HTML is produced via shiki, not arbitrary user input */
import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { createSignal, Show } from 'solid-js'
import { useSharedExpandedState } from '../../../messageRenderers'
import { renderBashHighlight, ToolUseLayout } from '../../../toolRenderers'
import { toolResultContentAnsi } from '../../../toolStyles.css'

/**
 * Inner component for tool_use messages. Renders the header and, for Bash,
 * an expandable multi-line command body. Edit/Write diffs live on the result
 * message — see renderClaudeToolResult.
 */
export function ToolUseMessage(props: {
  toolName: string
  icon: LucideIcon
  /** Header title (e.g. file path + line range, command description). */
  title: JSX.Element | null
  /** Summary shown below header inside the bordered area (e.g. Bash command, Grep result count). */
  summary?: JSX.Element | null
  /** Full command text for Bash (shown when expanded). */
  fullCommand?: string
  fallbackDisplay: string | null
  context?: RenderContext
}): JSX.Element {
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, 'tool-use-layout', false)
  const [commandCopied, setCommandCopied] = createSignal(false)

  const resolvedTitle = () => props.title ?? `${props.toolName}${props.fallbackDisplay || ''}`

  // Bash: collapsible when command is multi-line.
  const isMultiLineCommand = () => !!props.fullCommand && props.fullCommand.includes('\n')

  return (
    <ToolUseLayout
      icon={props.icon}
      toolName={props.toolName}
      title={resolvedTitle()}
      summary={isMultiLineCommand() && expanded() ? undefined : props.summary}
      context={props.context}
      expanded={expanded()}
      onToggleExpand={isMultiLineCommand() ? () => setExpanded(v => !v) : undefined}
      expandLabel="Show full command"
      headerActions={{
        onCopyContent: props.fullCommand
          ? () => {
              navigator.clipboard.writeText(props.fullCommand!)
              setCommandCopied(true)
              setTimeout(setCommandCopied, 2000, false)
            }
          : undefined,
        contentCopied: commandCopied(),
        copyContentLabel: 'Copy Command',
      }}
    >
      <Show when={isMultiLineCommand() && expanded()}>
        <div class={toolResultContentAnsi} innerHTML={renderBashHighlight(props.fullCommand!)} />
      </Show>
    </ToolUseLayout>
  )
}
