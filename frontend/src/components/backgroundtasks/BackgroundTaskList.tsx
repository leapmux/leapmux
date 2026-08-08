import type { Component, JSX } from 'solid-js'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import Circle from 'lucide-solid/icons/circle'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import OctagonMinus from 'lucide-solid/icons/octagon-minus'
import RotateCcw from 'lucide-solid/icons/rotate-ccw'
import Terminal from 'lucide-solid/icons/terminal'
import X from 'lucide-solid/icons/x'
import { For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  groupBackgroundTasks,
  isActiveBackgroundTaskStatus,
  sortBackgroundTasks,
} from '~/stores/chatBackgroundTasks'
import * as styles from './BackgroundTaskList.css'

interface BackgroundTaskListProps {
  tasks: BackgroundTaskItem[]
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  resolveParentLabel?: (agentId: string) => string | undefined
  /** Extra class for the root list (e.g. the popover scroll variant). */
  class?: string
}

/**
 * BackgroundTaskList renders the background-task registry for the sidebar
 * section and the ThinkingIndicator popover. Shared by both surfaces. Sorts
 * active-first (running before pending), groups by workflow/phase, and renders
 * a status glyph + title + secondary line per row. Subagent rows with a
 * childAgentId are clickable buttons; shell rows are static.
 */
export const BackgroundTaskList: Component<BackgroundTaskListProps> = (props) => {
  const sorted = () => sortBackgroundTasks(props.tasks)
  const grouped = () => groupBackgroundTasks(sorted())

  const statusGlyph = (item: BackgroundTaskItem): JSX.Element => {
    switch (item.status) {
      case 'running':
        return <LoaderCircle class={`${styles.taskIcon} ${styles.spinIcon}`} size={14} />
      case 'pending':
        return <Circle class={styles.taskIcon} size={14} />
      case 'completed':
        return <Check class={styles.taskIcon} size={14} />
      case 'failed':
        return <X class={styles.taskIcon} size={14} />
      case 'stopped':
        return <OctagonMinus class={styles.taskIcon} size={14} />
      case 'interrupted':
        return <RotateCcw class={styles.taskIcon} size={14} />
      default:
        return <Circle class={styles.taskIcon} size={14} />
    }
  }

  const kindIcon = (item: BackgroundTaskItem): JSX.Element => {
    if (item.kind === 'shell')
      return <Terminal class={styles.taskIcon} size={14} />
    return <Bot class={styles.taskIcon} size={14} />
  }

  const secondary = (item: BackgroundTaskItem): string => {
    if (isActiveBackgroundTaskStatus(item.status))
      return item.activity || item.description || ''
    return backgroundTaskEndLabel(item.status)
  }

  // Explanatory tooltip for a terminal status whose bare label is ambiguous
  // (e.g. "Interrupted" really means the worker/agent process restarted).
  const secondaryTooltip = (item: BackgroundTaskItem): string | undefined => {
    if (isActiveBackgroundTaskStatus(item.status))
      return undefined
    return backgroundTaskEndTooltip(item.status)
  }

  const renderSecondary = (item: BackgroundTaskItem): JSX.Element => {
    const text = secondary(item)
    if (!text)
      return null
    const tip = secondaryTooltip(item)
    if (!tip)
      return <span class={styles.taskSecondary}>{text}</span>
    return (
      <Tooltip text={tip}>
        <span class={styles.taskSecondary}>{text}</span>
      </Tooltip>
    )
  }

  const parentLabel = (item: BackgroundTaskItem): string | undefined => {
    if (!item.parentAgentId || !props.resolveParentLabel)
      return undefined
    const label = props.resolveParentLabel(item.parentAgentId)
    return label
  }

  const renderRow = (item: BackgroundTaskItem): JSX.Element => {
    const clickable = item.kind === 'subagent' && !!item.childAgentId && !!props.onOpenSubagent
    const onClick = () => {
      if (clickable && props.onOpenSubagent)
        props.onOpenSubagent(item)
    }
    const pLabel = parentLabel(item)
    return (
      <Show
        when={clickable}
        fallback={(
          <div
            class={styles.taskRow}
            classList={{ [styles.taskStruck]: !isActiveBackgroundTaskStatus(item.status) }}
            data-testid="bg-task-row"
            data-status={item.status}
            data-kind={item.kind}
            data-child-agent-id={item.childAgentId ?? ''}
          >
            {statusGlyph(item)}
            {kindIcon(item)}
            <div class={styles.taskBody}>
              <span class={styles.taskTitle}>{item.title || item.description || item.rowKey}</span>
              {renderSecondary(item)}
            </div>
            <Show when={pLabel}>
              <span class={styles.parentChip}>{`via ${pLabel}`}</span>
            </Show>
          </div>
        )}
      >
        <button
          type="button"
          class={styles.taskRow}
          classList={{ [styles.taskStruck]: !isActiveBackgroundTaskStatus(item.status) }}
          onClick={onClick}
          data-testid="bg-task-row"
          data-status={item.status}
          data-kind={item.kind}
          data-child-agent-id={item.childAgentId ?? ''}
        >
          {statusGlyph(item)}
          {kindIcon(item)}
          <div class={styles.taskBody}>
            <span class={styles.taskTitle}>{item.title || item.description || item.rowKey}</span>
            {renderSecondary(item)}
          </div>
          <Show when={pLabel}>
            <span class={styles.parentChip}>{`via ${pLabel}`}</span>
          </Show>
        </button>
      </Show>
    )
  }

  return (
    <div class={`${styles.taskList} ${props.class ?? ''}`}>
      <For each={grouped().ungrouped}>{item => renderRow(item)}</For>
      <For each={grouped().groups}>
        {group => (
          <>
            <div class={styles.groupHeader}>{group.label}</div>
            <For each={group.items}>{item => renderRow(item)}</For>
          </>
        )}
      </For>
    </div>
  )
}
