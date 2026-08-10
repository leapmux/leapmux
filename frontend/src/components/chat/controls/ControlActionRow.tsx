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
   * The left-end actions: reject, stop, stay-in-plan. Omit for a row that
   * offers only a primary action — the zone then collapses and the grid keeps
   * the primary action at the right end.
   */
  secondary?: JSX.Element
  /**
   * The centre zone, for a control that is neither a secondary nor a primary
   * action. Today only the multi-question pagination uses it.
   */
  centre?: JSX.Element
  /** The right-end actions: allow, submit, approve. */
  primary: JSX.Element
}

export const ControlActionRow: Component<ControlActionRowProps> = props => (
  <div class={styles.controlFooter} data-testid="control-footer">
    <Show when={props.secondary}>
      <div class={styles.controlFooterLeft}>{props.secondary}</div>
    </Show>
    {props.centre}
    <div class={styles.controlFooterRight}>{props.primary}</div>
  </div>
)
