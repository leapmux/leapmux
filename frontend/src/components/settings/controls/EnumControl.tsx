import type { Component } from 'solid-js'
import type { PillOptions } from '~/components/common/pillOptions'
import { Show } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { PillGroup } from '~/components/common/PillGroup'
import { isPillOptions } from '~/components/common/pillOptions'
import * as styles from '../SettingRow.css'

export interface EnumOption {
  value: string
  label: string
  help?: string
}

export interface EnumControlProps {
  /** Accessible name for the pill group or the menu. */
  ariaLabel: string
  value: string
  options: EnumOption[]
  /** Commit the chosen value. */
  onChange: (value: string) => void | Promise<boolean | void>
}

function fixedPillOptions(options: readonly EnumOption[]): PillOptions<string> | undefined {
  const pills = options.map(option => ({ key: option.value, label: option.label }))
  return isPillOptions(pills) ? pills : undefined
}

/**
 * Render a short option list as a PillGroup with radio semantics.
 * Render a longer list through LoadingMenu, as the AGENTS.md dropdown rule requires.
 * The Show expression selects its branch again when the option count changes.
 *
 * Both branches derive selection from props.value.
 * A refused write therefore retains the prior selected value without a separate DOM repair.
 *
 * Display the selected option's help beneath either control.
 * The backend schema supplies that explanation for each enum value.
 * The radio or menu label alone cannot display the full explanation.
 */
export const EnumControl: Component<EnumControlProps> = (props) => {
  const selectedHelp = (): string | undefined =>
    props.options.find(o => o.value === props.value)?.help

  const pills = () => fixedPillOptions(props.options)
  return (
    <>
      <Show
        // An empty list uses the menu branch. `LoadingMenu` then shows the
        // disabled trigger that states this condition.
        when={pills()}
        fallback={(
          <LoadingMenu
            ariaLabel={props.ariaLabel}
            value={props.value}
            onChange={props.onChange}
            emptyLabel="No options"
            // Show a selection prompt when the value is empty but the menu has options.
            // A fresh installation or an unset enum value can reach this state.
            // The emptyLabel describes an absent option list and must not describe this populated menu.
            placeholder="Select an option..."
            options={props.options.map(o => ({ value: o.value, label: o.label }))}
            data-testid="enum-control-menu"
          />
        )}
      >
        {options => (
          <PillGroup
            label={props.ariaLabel}
            options={options()}
            selectedKey={props.value}
            onSelect={props.onChange}
          />
        )}
      </Show>
      <Show when={selectedHelp()}>
        {help => <div class={styles.helpText}>{help()}</div>}
      </Show>
    </>
  )
}
