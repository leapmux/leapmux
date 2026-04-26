/* eslint-disable solid/no-innerhtml -- HTML is produced via shiki, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { CommandResultSource } from '../../../results/commandResult'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Terminal from 'lucide-solid/icons/terminal'
import { createEffect, For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { useSharedExpandedState } from '../../../messageRenderers'
import { firstNonEmptyLine, formatDuration } from '../../../rendererUtils'
import { renderBashHighlight, ToolResultMessage, ToolUseLayout } from '../../../toolRenderers'
import {
  commandStreamContainer,
  commandStreamInteraction,
  toolInputSummary,
  toolResultContentPre,
} from '../../../toolStyles.css'
import { renderBashTitle } from '../../../toolTitleRenderers'
import { codexUnwrapCommand } from '../extractors/commandExecution'
import { extractItem } from '../renderHelpers'

const DIV_OPEN_RE = /<div\b/g
const DIV_CLOSE_RE = /<\/div>/g
const TOOL_USE_HEADER_CLASS_FRAGMENT = 'toolUseHeader__'

/**
 * Strip our own injected tool-use headers from a Codex `aggregatedOutput`
 * payload. The backend sometimes echoes the rendered tool-use chrome (an
 * HTML `<div>` block) back into the aggregated output stream; this walks the
 * matching depth-counted div-block and drops it.
 */
function stripToolUseHeaderFromOutput(output: string): string {
  if (!output.includes(TOOL_USE_HEADER_CLASS_FRAGMENT))
    return output

  const lines = output.split('\n')
  const kept: string[] = []

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line.includes(TOOL_USE_HEADER_CLASS_FRAGMENT)) {
      kept.push(line)
      continue
    }

    if (kept.length > 0 && kept.at(-1)?.includes('<div'))
      kept.pop()

    let depth = 1
    while (++i < lines.length) {
      const current = lines[i]
      depth += (current.match(DIV_OPEN_RE) || []).length
      depth -= (current.match(DIV_CLOSE_RE) || []).length
      if (depth <= 0)
        break
    }
  }

  return kept.join('\n')
}

/** Renders Codex commandExecution items using shared ToolUseLayout. */
export function codexCommandExecutionRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'commandExecution')
    return null

  const rawCommand = (item.command as string) || '(command)'
  const command = codexUnwrapCommand(rawCommand)
  const cwd = (item.cwd as string) || ''
  const status = (item.status as string) || ''
  const output = stripToolUseHeaderFromOutput((item.aggregatedOutput as string) || '')
  const exitCode = item.exitCode as number | null | undefined
  const durationMs = item.durationMs as number | null | undefined
  const isTerminal = status === 'completed' || status === 'failed'
  const hasError = status === 'failed' || (isTerminal && exitCode != null && exitCode !== 0)
  const liveStream = () => context?.commandStream?.() ?? []
  const hasLiveStream = () => liveStream().length > 0
  const [expanded, setExpanded] = useSharedExpandedState(() => context, 'codex-command-execution')
  createEffect(() => {
    if (hasLiveStream())
      setExpanded(true)
  })
  const displayCommand = firstNonEmptyLine(command) ?? command
  const title = renderBashTitle('Run command', command) || 'Run command'

  const statusParts = (): string => {
    const parts: string[] = []
    if (exitCode != null)
      parts.push(`exit ${exitCode}`)
    if (durationMs != null)
      parts.push(formatDuration(durationMs))
    return parts.join(' · ')
  }

  if (isTerminal) {
    const commandSource: CommandResultSource = {
      output,
      exitCode: exitCode ?? null,
      durationMs: durationMs ?? null,
      isError: hasError,
    }
    return (
      <ToolResultMessage
        resultContent={output}
        commandResult={commandSource}
        context={context}
      />
    )
  }

  return (
    <ToolUseLayout
      icon={Terminal}
      toolName="Command Execution"
      title={title}
      summary={(
        <>
          <div class={toolInputSummary} innerHTML={renderBashHighlight(displayCommand)} />
          <Show when={statusParts()}>
            <div class={toolInputSummary}>{statusParts()}</div>
          </Show>
        </>
      )}
      context={context}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
    >
      <Show when={cwd}>
        <div class={toolInputSummary}>
          cwd:
          {' '}
          {relativizePath(cwd, context?.workingDir, context?.homeDir)}
        </div>
      </Show>
      <Show
        when={hasLiveStream()}
        fallback={<Show when={output}><div class={toolResultContentPre}>{output}</div></Show>}
      >
        <div class={commandStreamContainer}>
          <For each={liveStream()}>
            {segment => (
              <div class={segment.kind === 'interaction' ? commandStreamInteraction : toolResultContentPre}>
                {segment.kind === 'interaction' ? `> ${segment.text}` : segment.text}
              </div>
            )}
          </For>
        </div>
      </Show>
    </ToolUseLayout>
  )
}
