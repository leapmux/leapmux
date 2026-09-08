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

/**
 * The class every action button in the composer's footer slot carries — the
 * control-request decisions here, and the composer's own Pause, Interrupt and
 * Send.
 *
 * Oat's `.small` supplies the metrics (`--text-8`, and
 * `var(--space-1) var(--space-3)` of padding), which the `CompactSwitch` beside
 * them and the `PillGroup` `small` variant match. It is the ONE source for that
 * size: `--editor-btn-h` is derived from it so the `[+]` button matches, and the
 * slot's own rule in `~/components/chat/markdownEditor/MarkdownEditor.css.ts`
 * deliberately states no size, because an unlayered rule there would outrank
 * this class and could never lose to it.
 *
 * One function rather than a literal at each button, so a button added to the
 * slot later cannot fall back to Oat's full-size metrics by omission.
 */
export function actionButtonClass(outline?: boolean): string {
  return outline === true ? 'outline small' : 'small'
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
