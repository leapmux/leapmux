import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { ToolHeaderActionsCallerProps } from '../messageActions'
import type { RenderContext } from '../messageRenderers'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { children, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { inlineFlex } from '~/styles/shared.css'
import { ToolHeaderActions } from '../ToolHeaderActions'
import { toolBodyBorder, toolBodyContent, toolInputText, toolMessage, toolUseHeader, toolUseIcon } from '../toolStyles.css'
import { spanColorKey } from './SpanLines'
import { spanLineColors } from './SpanLines.css'
import { ToolRunningBadge } from './ToolRunningBadge'

function hasContent(values: JSX.Element[]): boolean {
  return values.some(value => typeof value === 'number' || (!!value && typeof value !== 'boolean'))
}

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
  /** False when the message host supplies these same actions in its toolbar. */
  showHeaderActions?: boolean
}): JSX.Element {
  const expanded = () => props.expanded ?? false
  const summary = children(() => props.summary)
  const body = children(() => props.children)
  const showBody = () => (props.alwaysVisible || expanded()) && hasContent(body.toArray())
  const actions = () => props.headerActions
  const hasActions = () =>
    props.showHeaderActions !== false && (!!props.onToggleExpand || !!props.context?.onCopyJson || !!props.hasDiff || !!actions()?.onCopyContent || !!actions()?.onCopyMarkdown || !!actions()?.onReply)
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
          toolProgress={props.context?.sources?.progress}
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
      <Show when={hasContent(summary.toArray()) || showBody()}>
        <div class={[
          toolBodyContent,
          props.bordered !== false && toolBodyBorder,
          props.bordered !== false
          && props.context?.spanColor != null
          && props.context.spanColor > 0
          && spanLineColors[spanColorKey(props.context.spanColor)],
        ].filter(Boolean).join(' ')}
        >
          {summary()}
          <Show when={showBody()}>
            {body()}
          </Show>
        </div>
      </Show>
    </div>
  )
}
