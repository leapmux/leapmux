import type { Component } from 'solid-js'
import type { BannerActionsProps, BannerContentProps } from '~/components/chat/controls/types'
import { ControlRequestActions as BannerActions, ControlRequestContent as BannerContent } from '~/components/chat/ControlRequestBanner'
import { createControlSurface } from '~/components/chat/controls/controlSurface'

/**
 * The two banner halves, with the surface that the composer supplies in the app.
 *
 * `ControlRequestContent` and `ControlRequestActions` classify nothing: they
 * take the surface and the resolved provider as props, because the composer
 * derives both ONCE for a request that mounts the two halves in two different
 * slots. A test renders one half as its own root, with no composer above it, so
 * these wrappers derive the same two values through the same
 * `createControlSurface` that `useControlResponseHandling` uses.
 *
 * A test therefore still exercises the real classifier and the real source
 * loading, and it passes `agentProvider` as the AGENT's provider, which is what
 * the composer passes and what every caller here already passed.
 */
type HarnessContentProps = Omit<BannerContentProps, 'controlSurface'>
type HarnessActionsProps = Omit<BannerActionsProps, 'controlSurface'>

export const ControlRequestContent: Component<HarnessContentProps> = (props) => {
  const live = createControlSurface(() => props.request, () => props.messageContext, () => props.agentProvider)
  // One narrowed read: spreading the call's result directly would carry an
  // explicit undefined into `agentProvider?: AgentProvider`, which
  // exactOptionalPropertyTypes refuses.
  const provider = live.provider()
  return (
    <BannerContent
      {...props}
      {...(provider === undefined ? {} : { agentProvider: provider })}
      controlSurface={live.surface()}
    />
  )
}

export const ControlRequestActions: Component<HarnessActionsProps> = (props) => {
  const live = createControlSurface(() => props.request, () => props.messageContext, () => props.agentProvider)
  const provider = live.provider()
  return (
    <BannerActions
      {...props}
      {...(provider === undefined ? {} : { agentProvider: provider })}
      controlSurface={live.surface()}
    />
  )
}
