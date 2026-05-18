import type { JSX } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { TaskCheckboxStatus } from '~/components/todo/TaskCheckbox'
import type { CLAUDE_TOOL } from '~/types/toolMessages'
import { TaskCheckbox } from '~/components/todo/TaskCheckbox'
import { todoStruck } from '~/components/todo/TodoList.css'
import { isTerminalTodoStatus, todoDisplayLabel } from '~/stores/chat.store'
import * as styles from './taskCardMessage.css'
import { ToolUseLayout } from './toolRenderers'
import { toolInputText } from './toolStyles.css'

/** Subset of CLAUDE_TOOL values that render through the single-row Task card. */
export type TaskCardToolName
  = | typeof CLAUDE_TOOL.TASK_CREATE
    | typeof CLAUDE_TOOL.TASK_UPDATE
    | typeof CLAUDE_TOOL.TASK_GET

/** Source shape for the single-row Claude Task* cards. */
export interface TaskCardSource {
  toolName: TaskCardToolName
  subject: string
  description?: string
  status: TaskCheckboxStatus
  activeForm?: string
}

export function TaskCardMessage(props: {
  source: TaskCardSource
  context?: RenderContext
}): JSX.Element {
  const titleLabel = () => todoDisplayLabel({
    status: props.source.status,
    content: props.source.subject,
    activeForm: props.source.activeForm,
  })
  return (
    <ToolUseLayout
      renderIcon={() => <TaskCheckbox status={props.source.status} />}
      toolName={props.source.toolName}
      title={(
        <span
          class={toolInputText}
          classList={{ [todoStruck]: isTerminalTodoStatus(props.source.status) }}
        >
          {titleLabel()}
        </span>
      )}
      summary={props.source.description ? <div class={styles.summary}>{props.source.description}</div> : undefined}
      bordered={false}
      context={props.context}
    />
  )
}
