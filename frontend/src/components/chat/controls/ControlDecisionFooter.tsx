import type { Accessor, Component } from 'solid-js'
import type { ControlPermissionPill } from './permissionPresets'

import type { PillOptions } from '~/components/common/PillGroup'
import { For, Index, Show } from 'solid-js'
import { CompactSwitch } from '~/components/common/CompactSwitch'
import { PillGroup } from '~/components/common/PillGroup'
import { keepFocusOnPress } from '~/lib/focusRetention'
import * as styles from '../ControlRequestBanner.css'
import { ControlActionRow } from './ControlActionRow'

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

/**
 * The permission pill group a control request's decision row offers: Default /
 * Smart permissions / Bypass permissions, one row segment shared by the decision
 * footer and the providers that lay out their own action row (ACP, OpenCode).
 */
export const ControlPermissionPillGroup: Component<{ pill: ControlPermissionPill }> = props => (
  <div class={styles.controlRequestPill} data-testid="control-permissions-pill-group">
    <PillGroup
      label="Permissions"
      options={props.pill.options}
      selectedKey={props.pill.selected}
      onSelect={props.pill.onSelect}
    />
  </div>
)

/**
 * The allow-scope pill group a permission request offers when one allow-once
 * faces two or more allow-always scopes (Once / Session / Project): the scope
 * control that REPLACES the Remember switch there, keys being optionIds so a
 * selection maps straight onto the wire reply.
 */
export const ControlAllowScopePillGroup: Component<{
  options: PillOptions<string>
  selected: string
  onSelect: (optionId: string) => void
}> = props => (
  <div class={styles.controlRequestPill} data-testid="control-allow-scope-pill-group">
    <PillGroup
      label="Allow scope"
      options={props.options}
      selectedKey={props.selected}
      onSelect={props.onSelect}
    />
  </div>
)

/** Renders the shared options and decision layout for a control request. */
export const ControlDecisionFooter: Component<{
  hasEditorContent: boolean
  onSendFeedback: () => void
  negativeAction: ControlDecisionAction
  positiveAction: ControlDecisionAction
  switches?: Accessor<ControlRequestSwitch[]>
  /** The permission pill group, or omit it when no preset is available. */
  permissionPill?: Accessor<ControlPermissionPill | undefined>
  additionalActions?: Accessor<ControlDecisionAction[]>
}> = (props) => {
  const switches = () => props.switches?.() ?? []
  const additionalActions = () => props.additionalActions?.() ?? []
  // One leading cluster -- [switches][permission pill] -- so the row reads as
  // options followed by decisions: a pill is button-high, so it shares the row
  // with the switches instead of stacking above them.
  const leadingOptions = () => switches().length > 0 || !!props.permissionPill?.()

  return (
    <ControlActionRow
      primary={(
        <>
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
              <Show when={props.permissionPill?.()}>
                {pill => <ControlPermissionPillGroup pill={pill()} />}
              </Show>
            </div>
          </Show>
          <button
            class="outline"
            onMouseDown={keepFocusOnPress}
            onClick={() => props.hasEditorContent ? props.onSendFeedback() : props.negativeAction.onSelect()}
            data-testid={props.negativeAction.testId}
          >
            {props.hasEditorContent ? 'Send feedback' : props.negativeAction.label}
          </button>
          <Show when={!props.hasEditorContent}>
            <button
              class={props.positiveAction.outline ? 'outline' : undefined}
              onClick={props.positiveAction.onSelect}
              data-testid={props.positiveAction.testId}
            >
              {props.positiveAction.label}
            </button>
            <For each={additionalActions()}>
              {decision => (
                <button
                  class={decision.outline ? 'outline' : undefined}
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
