import type { Accessor, Component, JSX } from 'solid-js'
import type { ControlAllowChoicePill } from './ControlPillGroups'
import type { ControlPermissionPill } from './permissionPresets'

import { Index, Show } from 'solid-js'
import { CompactSwitch } from '~/components/common/CompactSwitch'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { moreHorizontalTrigger } from '~/components/common/moreHorizontalTrigger'
import { Tooltip } from '~/components/common/Tooltip'
import { keepFocusOnPress } from '~/lib/focusRetention'
import { dangerMenuItem } from '~/styles/shared.css'
import { actionButtonClass, ControlActionRow } from './ControlActionRow'
import { ControlAllowChoicePillGroup, ControlPermissionPillGroup } from './ControlPillGroups'
import { invokeControlAction } from './controlResponseError'

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
  /**
   * The action REFUSES the request. The overflow menu draws it in the danger
   * colour, so "Reject always" does not read as one more way to allow.
   *
   * It replaces an `outline` flag that said how the button looked rather than
   * what it did. That flag lost its last setter when the extra options moved
   * into the menu, and every extra then looked identical.
   */
  destructive?: boolean
  disabled?: boolean
  /**
   * What the action does, when its label does not say it all. The overflow menu
   * shows it as the item's tooltip, as the settings menu shows an option's
   * description.
   */
  description?: string
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
  error?: string
  /** Additional request controls before the decisions. */
  leading?: JSX.Element
}> = (props) => {
  const switches = () => props.switches?.() ?? []
  const additionalActions = () => props.additionalActions?.() ?? []
  // Options precede decisions. The shared action row controls their wrapping.
  const leadingOptions = () => switches().length > 0 || !!props.allowChoicePill?.() || !!props.permissionPill?.()

  return (
    <ControlActionRow
      leading={(
        <>
          {props.leading}
          <Show when={!props.hasEditorContent && leadingOptions()}>
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
          </Show>
          <Show when={props.error}><span role="alert">{props.error}</span></Show>
        </>
      )}
      primary={(
        <>
          <Show when={!props.hasEditorContent && additionalActions().length}>
            <DropdownMenu trigger={moreHorizontalTrigger({ 'title': 'More actions', 'data-testid': 'control-more-actions' })} aria-label="More actions">
              <Index each={additionalActions()}>
                {(decision) => {
                  const item = () => (
                    <button
                      type="button"
                      role="menuitem"
                      class={decision().destructive ? dangerMenuItem : undefined}
                      disabled={decision().disabled}
                      onClick={() => invokeControlAction(decision().onSelect)}
                      data-testid={decision().testId}
                    >
                      {decision().label}
                    </button>
                  )
                  // Wrap only when there is a description. A Tooltip mounts its own
                  // wrapper and listeners even with nothing to show.
                  return (
                    <Show when={decision().description} fallback={item()}>
                      {description => <Tooltip text={description()}>{item()}</Tooltip>}
                    </Show>
                  )
                }}
              </Index>
            </DropdownMenu>
          </Show>
          <Show when={props.hasEditorContent || props.negativeAction}>
            <button
              class={actionButtonClass(true)}
              disabled={props.negativeAction?.disabled}
              onMouseDown={keepFocusOnPress}
              onClick={() => invokeControlAction(() => props.hasEditorContent ? props.onSendFeedback() : props.negativeAction?.onSelect())}
              data-testid={props.negativeAction?.testId ?? 'control-deny-btn'}
            >
              {props.hasEditorContent ? 'Send feedback' : props.negativeAction?.label}
            </button>
          </Show>
          <Show when={!props.hasEditorContent}>
            <Show when={props.positiveAction}>
              {action => (
                <button
                  class={actionButtonClass()}
                  disabled={action().disabled}
                  onClick={() => invokeControlAction(action().onSelect)}
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
