import type { Accessor, Component } from 'solid-js'
import type { ControlRequestSwitch } from './ControlRequestSwitches'

import { For, Show } from 'solid-js'
import { keepFocusOnPress } from '~/lib/focusRetention'
import { ControlActionRow } from './ControlActionRow'
import { ControlRequestSwitches } from './ControlRequestSwitches'

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
  negativeAction: ControlDecisionAction
  positiveAction: ControlDecisionAction
  switches?: Accessor<ControlRequestSwitch[]>
  additionalActions?: Accessor<ControlDecisionAction[]>
}> = (props) => {
  const switches = () => props.switches?.() ?? []
  const additionalActions = () => props.additionalActions?.() ?? []

  return (
    <ControlActionRow
      primary={(
        <>
          <Show when={!props.hasEditorContent && switches().length > 0}>
            <ControlRequestSwitches items={switches()} />
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
