import type { Accessor, Component } from 'solid-js'
import type { ControlAllowChoicePill } from './ControlPillGroups'
import type { ControlPermissionPill } from './permissionPresets'

import { For, Index, Show } from 'solid-js'
import { CompactSwitch } from '~/components/common/CompactSwitch'
import { keepFocusOnPress } from '~/lib/focusRetention'
import * as styles from '../ControlRequestBanner.css'
import { actionButtonClass, ControlActionRow } from './ControlActionRow'
import { ControlAllowChoicePillGroup, ControlPermissionPillGroup } from './ControlPillGroups'

export interface ControlRequestSwitch {
  id: string
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
  suffix?: string
}

export interface ControlDecisionAction {
  label: string
  testId: string
  onSelect: () => void | Promise<void>
  outline?: boolean
}

/** Renders the shared options and decision layout for a control request. */
export const ControlDecisionFooter: Component<{
  hasEditorContent: boolean
  onSendFeedback: () => void
  /**
   * The refusal. OMIT it only when the provider offered no way to refuse: the
   * slot then draws nothing, rather than a button that sends a decision the
   * request never carried. `hasEditorContent` still turns the slot into Send
   * feedback, which needs no decision of its own.
   */
  negativeAction?: ControlDecisionAction
  /** The approval. Omit it when the provider offered no way to approve. */
  positiveAction?: ControlDecisionAction
  switches?: Accessor<ControlRequestSwitch[]>
  /** The provider-derived choices for how the positive action grants access. */
  allowChoicePill?: Accessor<ControlAllowChoicePill | undefined>
  /** The permission pill group, or omit it when no preset is available. */
  permissionPill?: Accessor<ControlPermissionPill | undefined>
  additionalActions?: Accessor<ControlDecisionAction[]>
}> = (props) => {
  const switches = () => props.switches?.() ?? []
  const additionalActions = () => props.additionalActions?.() ?? []
  // One leading cluster -- [switches][allow-choice pill][permission pill] -- so
  // the row reads as options followed by decisions. Each pill is button-high, so
  // the pills share the row with the switches instead of stacking above them.
  // The order runs from what this request grants to what the session keeps, and
  // `controlDecisionFooter renders the leading cluster in one order` pins it.
  const leadingOptions = () => switches().length > 0 || !!props.allowChoicePill?.() || !!props.permissionPill?.()

  return (
    <ControlActionRow
      leading={(
        <Show when={!props.hasEditorContent && leadingOptions()}>
          <div class={styles.controlRequestSwitches}>
            <Index each={switches()}>
              {item => (
                <CompactSwitch
                  checked={item().checked}
                  onChange={item().onChange}
                  data-testid={item().id}
                  fontSize="var(--text-8)"
                >
                  {item().label}
                  {item().suffix}
                </CompactSwitch>
              )}
            </Index>
            <Show when={props.allowChoicePill?.()}>
              {pill => <ControlAllowChoicePillGroup pill={pill()} />}
            </Show>
            <Show when={props.permissionPill?.()}>
              {pill => <ControlPermissionPillGroup pill={pill()} />}
            </Show>
          </div>
        </Show>
      )}
      primary={(
        <>
          <Show when={props.hasEditorContent || props.negativeAction}>
            <button
              class={actionButtonClass(true)}
              onMouseDown={keepFocusOnPress}
              onClick={() => props.hasEditorContent ? props.onSendFeedback() : props.negativeAction?.onSelect()}
              data-testid={props.negativeAction?.testId ?? 'control-deny-btn'}
            >
              {props.hasEditorContent ? 'Send feedback' : props.negativeAction?.label}
            </button>
          </Show>
          <Show when={!props.hasEditorContent}>
            <Show when={props.positiveAction}>
              {action => (
                <button
                  class={actionButtonClass(action().outline)}
                  onClick={action().onSelect}
                  data-testid={action().testId}
                >
                  {action().label}
                </button>
              )}
            </Show>
            <For each={additionalActions()}>
              {decision => (
                <button
                  class={actionButtonClass(decision.outline)}
                  onClick={decision.onSelect}
                  data-testid={decision.testId}
                >
                  {decision.label}
                </button>
              )}
            </For>
          </Show>
        </>
      )}
    />
  )
}
