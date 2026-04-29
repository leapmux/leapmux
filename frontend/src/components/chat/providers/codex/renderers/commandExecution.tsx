/* eslint-disable solid/no-innerhtml -- HTML is produced via shiki, not arbitrary user input */
import type { CommandResultSource } from '../../../results/commandResult'
import Terminal from 'lucide-solid/icons/terminal'
import { createEffect, createMemo, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { CODEX_ITEM } from '~/types/toolMessages'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { firstNonEmptyLine, formatDuration, joinMetaParts } from '../../../rendererUtils'
import { renderBashHighlight, ToolResultMessage, ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultContentPre } from '../../../toolStyles.css'
import { renderBashTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexCommandFromItem, codexUnwrapCommand } from '../extractors/commandExecution'
import { LiveStreamOutput } from '../renderHelpers'
import { isCodexTerminalStatus, readLiveStream } from '../status'

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

// Registry-only: dispatched by `item.type === 'commandExecution'` via
// `CODEX_RENDERERS` (loaded from `renderers/registerAll.ts`).
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.COMMAND_EXECUTION],
  render: (props) => {
    // baseSource + stripped output are the hot work — only re-run when
    // `props.item` changes, not on UI-only re-renders (expand toggle).
    const baseSource = createMemo(() => codexCommandFromItem(props.item))
    const output = createMemo(() => stripToolUseHeaderFromOutput(baseSource()?.output ?? ''))

    const command = createMemo(() => codexUnwrapCommand((props.item.command as string) || '(command)'))
    const cwd = (): string => (props.item.cwd as string) || ''
    const isTerminal = (): boolean => isCodexTerminalStatus((props.item.status as string) || '')
    const liveStream = () => readLiveStream(props.context)
    const hasLiveStream = (): boolean => liveStream().length > 0

    const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.CODEX_COMMAND_EXECUTION)
    createEffect(() => {
      if (hasLiveStream())
        setExpanded(true)
    })

    const displayCommand = createMemo(() => firstNonEmptyLine(command()) ?? command())
    const title = createMemo(() => renderBashTitle('Run command', command()) || 'Run command')

    const statusParts = createMemo(() => {
      const code = baseSource()?.exitCode ?? null
      const dur = baseSource()?.durationMs ?? null
      return joinMetaParts([
        code != null && `exit ${code}`,
        dur != null && formatDuration(dur),
      ])
    })

    return (
      <Show
        when={isTerminal() && baseSource()}
        fallback={(
          <ToolUseLayout
            icon={Terminal}
            toolName="Command Execution"
            title={title()}
            summary={(
              <>
                <div class={toolInputSummary} innerHTML={renderBashHighlight(displayCommand())} />
                <Show when={statusParts()}>
                  <div class={toolInputSummary}>{statusParts()}</div>
                </Show>
              </>
            )}
            context={props.context}
            expanded={expanded()}
            onToggleExpand={() => setExpanded(v => !v)}
          >
            <Show when={cwd()}>
              <div class={toolInputSummary}>
                cwd:
                {' '}
                {relativizePath(cwd(), props.context?.workingDir, props.context?.homeDir)}
              </div>
            </Show>
            <Show
              when={hasLiveStream()}
              fallback={<Show when={output()}><div class={toolResultContentPre}>{output()}</div></Show>}
            >
              <LiveStreamOutput stream={liveStream} />
            </Show>
          </ToolUseLayout>
        )}
      >
        {(base) => {
          const commandSource = (): CommandResultSource => ({ ...base(), output: output() })
          return (
            <ToolResultMessage
              resultContent={output()}
              commandResult={commandSource()}
              context={props.context}
            />
          )
        }}
      </Show>
    )
  },
})
