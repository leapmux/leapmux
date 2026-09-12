import type { Component, JSX } from 'solid-js'
import { children, Show } from 'solid-js'
import { compactControl } from '~/components/common/CompactControl.css'
import * as styles from '../ControlRequestBanner.css'

/** Providers supply their native actions to one shared, wrapping row. */
export interface ControlActionRowProps {
  /** Actions that do not answer the request, such as Stop and YOLO. */
  secondary?: JSX.Element
  /** Question pagination. */
  navigation?: JSX.Element
  /** Options that qualify the decision. */
  leading?: JSX.Element
  /** Decisions on the request. */
  primary: JSX.Element
}

/** Action buttons use the compact metrics that switches and small pill groups share. */
export function actionButtonClass(outline?: boolean): string {
  return outline === true ? `${compactControl} outline` : compactControl
}

export const ControlActionRow: Component<ControlActionRowProps> = (props) => {
  const secondary = children(() => props.secondary)
  const navigation = children(() => props.navigation)
  const leading = children(() => props.leading)
  const primary = children(() => props.primary)
  return (
    <div class={styles.controlFooter} data-testid="control-footer">
      <Show when={secondary.toArray().length > 0}>
        <div class={styles.controlFooterSecondary}>{secondary()}</div>
      </Show>
      <Show when={navigation.toArray().length > 0}>
        <div class={styles.controlFooterNavigation}>{navigation()}</div>
      </Show>
      <Show when={leading.toArray().length > 0 || primary.toArray().length > 0}>
        <div class={styles.controlFooterDecisions}>
          <Show when={leading.toArray().length > 0}>
            <div class={styles.controlRequestSwitches}>{leading()}</div>
          </Show>
          {primary()}
        </div>
      </Show>
    </div>
  )
}
