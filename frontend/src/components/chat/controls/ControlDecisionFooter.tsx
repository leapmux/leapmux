import type { Accessor, Component } from 'solid-js'
import type { ControlAllowChoicePill } from './ControlPillGroups'
import type { ControlPermissionPill } from './permissionPresets'

import { For, Index, Show } from 'solid-js'
import { CompactSwitch } from '~/components/common/CompactSwitch'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { moreHorizontalTrigger } from '~/components/common/moreHorizontalTrigger'
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
   * Omit the refusal only when the provider offers no refusal decision.
   * Editor content replaces this action with Send feedback, which needs no provider decision.
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
  // Options precede decisions on one row: switches, request choices, then session permissions.
  // The leading-controls test checks this order for every provider.
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
          <Show when={!props.hasEditorContent && additionalActions().length}>
            <DropdownMenu trigger={moreHorizontalTrigger({ 'title': 'More actions', 'data-testid': 'control-more-actions' })} aria-label="More actions">
              <For each={additionalActions()}>
                {decision => (
                  <button type="button" role="menuitem" onClick={decision.onSelect} data-testid={decision.testId}>
                    {decision.label}
                  </button>
                )}
              </For>
            </DropdownMenu>
          </Show>
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
          </Show>
        </>
      )}
    />
  )
}
