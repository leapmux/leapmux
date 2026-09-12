import type { CommandResultSource } from '../../../results/commandResult'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { CODEX_ITEM } from '~/types/toolMessages'
import { messageCompletionFromProto } from '../../../assembledMessage'
import { cachedRenderValue } from '../../../messageRenderCache'
import { useSharedExpandedState } from '../../../messageRenderers'
import { MESSAGE_UI_KEY } from '../../../messageUiKeys'
import { formatDuration, joinMetaParts } from '../../../rendererUtils'
import { CommandInputSummary } from '../../../results/multiLineCommandBody'
import { ToolResultMessage, ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary, toolResultContentPre } from '../../../toolStyles.css'
import { renderBashTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import { codexCommandFromItem, codexUnwrapCommand } from '../extractors/commandExecution'
import { isCodexFinishedStatus } from '../status'

// Registry-only: dispatched by `item.type === 'commandExecution'` via
// `CODEX_RENDERERS` (loaded from `renderers/registerAll.ts`).
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.COMMAND_EXECUTION],
  render: (props) => {
    // Cache extraction across changes to expansion and other display state.
    const baseSource = createMemo(() => {
      const context = props.context
      const item = props.item
      return cachedRenderValue(context, 'codex.commandExecution.baseSource', () => codexCommandFromItem(item))
    })
    const output = () => baseSource()?.output ?? ''

    const command = createMemo(() => codexUnwrapCommand((props.item.command as string) || '(command)'))
    const cwd = (): string => (props.item.cwd as string) || ''
    const isFinished = (): boolean => !!messageCompletionFromProto(props.context?.sources?.current()?.completion) || isCodexFinishedStatus((props.item.status as string) || '')

    const [expanded, setExpanded] = useSharedExpandedState(() => props.context, MESSAGE_UI_KEY.CODEX_COMMAND_EXECUTION)
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
        when={isFinished() && baseSource()}
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
            <Show when={output()}><div class={toolResultContentPre}>{output()}</div></Show>
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
