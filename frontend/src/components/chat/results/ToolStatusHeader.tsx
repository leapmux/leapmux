import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { inlineFlex } from '~/styles/shared.css'
import { toolInputText, toolMessage, toolUseHeader, toolUseIcon } from '../toolStyles.css'

/**
 * Single-row icon + title header. Standalone — no outer `toolMessage` wrapper.
 * Use this when rendering an inline header inside an existing `ToolUseLayout`
 * body (e.g. an inline "Failed" indicator). For terminal-state tool result
 * bubbles, prefer {@link ToolStatusHeader} which adds the outer wrapper.
 */
export function ToolHeaderRow(props: {
  icon: LucideIcon
  title: JSX.Element | string
}): JSX.Element {
  return (
    <div class={toolUseHeader}>
      <span class={`${inlineFlex} ${toolUseIcon}`}>
        <Icon icon={props.icon} size="md" />
      </span>
      <span class={toolInputText}>{props.title}</span>
    </div>
  )
}

/**
 * Shared chrome for terminal-state tool result bubbles: outer `toolMessage`
 * wrapper, status icon, and a single-line title followed by optional body.
 *
 * `dataToolMessage` adds the `data-tool-message` attribute the
 * `MessageBubble.injectCopyButtons` injector uses to skip copy buttons on
 * shiki `<pre>` elements inside tool bubbles.
 */
export function ToolStatusHeader(props: {
  icon: LucideIcon
  title: JSX.Element | string
  dataToolMessage?: boolean
  children?: JSX.Element
}): JSX.Element {
  return (
    <div class={toolMessage} data-tool-message={props.dataToolMessage ? '' : undefined}>
      <ToolHeaderRow icon={props.icon} title={props.title} />
      {props.children}
    </div>
  )
}
