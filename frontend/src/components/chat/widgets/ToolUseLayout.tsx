import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { ToolHeaderActionsCallerProps } from '../messageActions'
import type { RenderContext } from '../messageRenderers'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { inlineFlex } from '~/styles/shared.css'
import { ToolHeaderActions } from '../ToolHeaderActions'
import { toolBodyBorder, toolBodyContent, toolInputText, toolMessage, toolUseHeader, toolUseIcon } from '../toolStyles.css'
import { spanColorKey } from './SpanLines'
import { spanLineColors } from './SpanLines.css'
import { ToolRunningBadge } from './ToolRunningBadge'

/** Render the common header and body for a tool-use message. */
export function ToolUseLayout(props: {
  icon?: LucideIcon
  renderIcon?: () => JSX.Element
  toolName: string
  title: string | JSX.Element
  summary?: JSX.Element
  children?: JSX.Element
  alwaysVisible?: boolean
  bordered?: boolean
  hasDiff?: boolean
  diffView?: DiffViewPreference
  onDiffViewChange?: (view: DiffViewPreference) => void
  context?: RenderContext
  expanded?: boolean
  onToggleExpand?: () => void
  expandLabel?: string
  headerActions?: ToolHeaderActionsCallerProps
}): JSX.Element {
  const expanded = () => props.expanded ?? false
  const actions = () => props.headerActions
  const hasActions = () =>
    !!props.onToggleExpand || !!props.context?.onCopyJson || !!props.hasDiff || !!actions()?.onCopyContent || !!actions()?.onCopyMarkdown || !!actions()?.onReply
  return (
    <div class={toolMessage} data-tool-message>
      <div class={toolUseHeader}>
        <Tooltip text={props.toolName} ariaLabel>
          <span class={`${inlineFlex} ${toolUseIcon}`}>
            {props.renderIcon
              ? props.renderIcon()
              : props.icon
                ? <Icon icon={props.icon} size="md" />
                : null}
          </span>
        </Tooltip>
        {typeof props.title === 'string'
          ? <span class={toolInputText}>{props.title}</span>
          : props.title}
        <ToolRunningBadge
          toolProgress={props.context?.toolProgress}
          textSelectionActive={props.context?.textSelectionActive}
        />
        <Show when={hasActions()}>
          <ToolHeaderActions
            caller={actions()}
            layout={{
              createdAt: props.context?.createdAt,
              expanded: expanded(),
              onToggleExpand: props.onToggleExpand,
              expandLabel: props.expandLabel,
              onCopyJson: props.context?.onCopyJson,
              jsonCopied: props.context?.jsonCopied?.() ?? false,
              hasDiff: props.hasDiff,
              diffView: props.diffView,
              onToggleDiffView: props.onDiffViewChange ? () => props.onDiffViewChange!(props.diffView === 'unified' ? 'split' : 'unified') : undefined,
            }}
          />
        </Show>
      </div>
      <Show when={props.summary || (props.children && (props.alwaysVisible || expanded()))}>
        <div class={[
          toolBodyContent,
          props.bordered !== false && toolBodyBorder,
          props.bordered !== false
          && props.context?.spanColor != null
          && props.context.spanColor > 0
          && spanLineColors[spanColorKey(props.context.spanColor)],
        ].filter(Boolean).join(' ')}
        >
          <Show when={props.summary}>{props.summary}</Show>
          <Show when={props.children && (props.alwaysVisible || expanded())}>
            {props.children}
          </Show>
        </div>
      </Show>
    </div>
  )
}
