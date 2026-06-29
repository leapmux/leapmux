import type { CommandResultSource } from '../../../results/commandResult'
import Terminal from 'lucide-solid/icons/terminal'
import { createEffect, createMemo, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { CODEX_ITEM } from '~/types/toolMessages'
import { cachedRenderValue } from '../../../messageRenderCache'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { formatDuration, joinMetaParts } from '../../../rendererUtils'
import { CommandInputSummary } from '../../../results/multiLineCommandBody'
import { ToolResultMessage, ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultContentPre } from '../../../toolStyles.css'
import { renderBashTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexCommandFromItem, codexUnwrapCommand, stripToolUseHeaderFromOutput } from '../extractors/commandExecution'
import { LiveStreamOutput } from '../renderHelpers'
import { isCodexTerminalStatus, readLiveStream } from '../status'

// Registry-only: dispatched by `item.type === 'commandExecution'` via
// `CODEX_RENDERERS` (loaded from `renderers/registerAll.ts`).
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.COMMAND_EXECUTION],
  render: (props) => {
    // baseSource + stripped output are the hot work — only re-run when
    // `props.item` changes, not on UI-only re-renders (expand toggle).
    const baseSource = createMemo(() => {
      const context = props.context
      const item = props.item
      return cachedRenderValue(context, 'codex.commandExecution.baseSource', () => codexCommandFromItem(item))
    })
    const output = createMemo(() => {
      const context = props.context
      const sourceOutput = baseSource()?.output ?? ''
      return cachedRenderValue(context, 'codex.commandExecution.strippedOutput', () => stripToolUseHeaderFromOutput(sourceOutput))
    })

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
                <CommandInputSummary
                  collapsed={!expanded()}
                  command={command()}
                  context={props.context}
                  namespace="codex.commandExecution.summary"
                />
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
