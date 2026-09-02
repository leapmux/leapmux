import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import * as styles from '../ControlRequestBanner.css'

/**
 * The action row a control request renders inside the composer box.
 *
 * Every provider's actions land here, so the row's shape is declared once: a
 * full-width three-zone grid of `[secondary | centre | primary]` with the
 * separator above it. Before this, eight components hand-wrote the same
 * `controlFooter` > `controlFooterRight` wrapper pair, most of them only to
 * reach the right-hand zone — and only two carried the `control-footer` test id,
 * so nothing noticed when one of them differed.
 *
 * The LAYOUT is shared; the BUTTONS are not. Each provider passes its own
 * actions as slot content, so nothing about a provider's wire format or its
 * decision vocabulary moves into this file.
 */
export interface ControlActionRowProps {
  /**
   * The left-end actions that are NOT a decision on the request: Stop, YOLO.
   *
   * A decision button belongs in `primary`, next to the one it opposes, however
   * it is worded — Reject, Cancel, or Deny. Several providers
   * emit their allow and deny buttons from ONE runtime list inside a connected
   * `ButtonGroup`, so a zone split by polarity would break that segmented control
   * and would put the same-named button at opposite ends of the row depending on
   * which provider answered.
   */
  secondary?: JSX.Element
  /**
   * The centre zone, for a control that is neither a secondary nor a primary
   * action. Today only the multi-question pagination uses it.
   */
  centre?: JSX.Element
  /** The right-end actions: the decision on the request. */
  primary: JSX.Element
}

export const ControlActionRow: Component<ControlActionRowProps> = props => (
  <div class={styles.controlFooter} data-testid="control-footer">
    <Show when={props.secondary}>
      <div class={styles.controlFooterLeft}>{props.secondary}</div>
    </Show>
    <Show when={props.centre}>
      <div class={styles.controlFooterCentre}>{props.centre}</div>
    </Show>
    <div class={styles.controlFooterRight}>{props.primary}</div>
  </div>
)
