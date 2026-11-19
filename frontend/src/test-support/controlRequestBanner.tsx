import type { Component } from 'solid-js'
import type { BannerActionsProps, BannerContentProps } from '~/components/chat/controls/types'
import { ControlRequestActions as BannerActions, ControlRequestContent as BannerContent } from '~/components/chat/ControlRequestBanner'
import { controlRequestProvider } from '~/stores/control.store'

/**
 * The two banner halves, with the prompt and the provider that the composer
 * supplies in the app.
 *
 * `ControlRequestContent` and `ControlRequestActions` classify nothing: they take
 * the request's prompt as a prop, and the actions take the resolved provider. A
 * test renders one half as its own root, with no composer above it, so these
 * wrappers pass the request's own prompt and resolve the provider as the composer
 * does. A test passes the actions' `agentProvider` as the AGENT's provider, which is
 * what the composer passes. Each prop is a getter, so a test that swaps the request
 * swaps both.
 */
type HarnessContentProps = Omit<BannerContentProps, 'controlSurface'>
type HarnessActionsProps = Omit<BannerActionsProps, 'controlSurface'>

export const ControlRequestContent: Component<HarnessContentProps> = props => (
  <BannerContent {...props} controlSurface={props.request?.prompt} />
)

export const ControlRequestActions: Component<HarnessActionsProps> = props => (
  <BannerActions
    {...props}
    agentProvider={controlRequestProvider(props.request, props.agentProvider)}
    controlSurface={props.request?.prompt}
  />
)
