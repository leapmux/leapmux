import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './controls/types'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import { createSignal, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import * as styles from './ControlRequestBanner.css'
import { getProviderPlugin } from './providers/registry'
import { prettifyJson } from './rendererUtils'

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<ContentProps> = (props) => {
  const pluginContent = () => props.agentProvider != null ? getProviderPlugin(props.agentProvider)?.ControlContent : undefined
  const [copied, setCopied] = createSignal(false)

  const handleCopyJson = () => {
    navigator.clipboard.writeText(prettifyJson(JSON.stringify(props.request?.payload)))
    setCopied(true)
    setTimeout(setCopied, 2000, false)
  }

  return (
    <Show when={props.request}>
      <div class={styles.controlBanner} data-testid="control-banner">
        <div class={styles.controlBannerActions} data-testid="control-banner-actions">
          <IconButton
            icon={copied() ? Check : Braces}
            size="sm"
            onClick={handleCopyJson}
            title={copied() ? 'Copied' : 'Copy Raw JSON'}
            data-testid="control-copy-json"
          />
        </div>
        <Dynamic component={pluginContent()} {...props} />
      </div>
    </Show>
  )
}

/** Renders control request action buttons only, for the footer slot. */
export const ControlRequestActions: Component<ActionsProps> = (props) => {
  const pluginActions = () => props.agentProvider != null ? getProviderPlugin(props.agentProvider)?.ControlActions : undefined
  return (
    <Show when={props.request}>
      <Dynamic component={pluginActions()} {...props} />
    </Show>
  )
}
