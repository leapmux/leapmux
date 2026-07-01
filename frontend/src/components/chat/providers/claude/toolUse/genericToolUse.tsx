import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { CommandInputBody, CommandInputSummary, createCommandInputExpansionState } from '../../../results/multiLineCommandBody'
import { ToolUseLayout } from '../../../toolRenderers'

/**
 * Inner component for tool_use messages. Renders the header and, for Bash, an
 * expandable full-command body. Edit/Write diffs live on the result message --
 * see renderClaudeToolResult.
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
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.TOOL_USE_LAYOUT)
  const { copied: commandCopied, copy: copyCommand } = useCopyButton(() => props.fullCommand)
  const { commandExpandable, setSummaryOverflows } = createCommandInputExpansionState(() => props.fullCommand)

  const resolvedTitle = () => props.title ?? `${props.toolName}${props.fallbackDisplay || ''}`

  const summary = () => props.fullCommand
    ? (
        <CommandInputSummary
          command={props.fullCommand}
          context={props.context}
          collapsed
          onOverflowChange={setSummaryOverflows}
        />
      )
    : props.summary

  return (
    <ToolUseLayout
      icon={props.icon}
      toolName={props.toolName}
      title={resolvedTitle()}
      summary={commandExpandable() && expanded() ? undefined : summary()}
      context={props.context}
      expanded={expanded()}
      onToggleExpand={commandExpandable() ? () => setExpanded(v => !v) : undefined}
      expandLabel="Show full command"
      headerActions={{
        onCopyContent: props.fullCommand ? copyCommand : undefined,
        contentCopied: commandCopied(),
        copyContentLabel: 'Copy Command',
      }}
    >
      <Show when={commandExpandable() && expanded()}>
        <CommandInputBody command={props.fullCommand!} context={props.context} />
      </Show>
    </ToolUseLayout>
  )
}
