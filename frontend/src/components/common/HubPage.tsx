import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { ContinueInBrowser } from '~/components/desktop/ContinueInBrowser'
import { ceremoniesRunInBrowser } from '~/lib/hubSite'

/**
 * Wraps a ceremony page that can only run at the Hub's origin (sign-in,
 * sign-up, recovery, email verification, step-up). In a browser and in
 * desktop solo mode the children render normally. In desktop remote-Hub
 * mode the children cannot complete — passkeys, OAuth and captcha need the
 * Hub's origin — so the `inDesktop` component renders instead.
 */
export const HubPage: Component<{
  inDesktop?: () => JSX.Element
  children: JSX.Element
}> = (props) => {
  return (
    <Show
      when={!ceremoniesRunInBrowser()}
      fallback={props.inDesktop
        ? props.inDesktop()
        : <ContinueInBrowser />}
    >
      {props.children}
    </Show>
  )
}
