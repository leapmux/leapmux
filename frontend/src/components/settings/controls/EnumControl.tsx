import type { Component } from 'solid-js'
import type { PillOptions, PillOptionSpec } from '~/components/common/PillGroup'
import { Show } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { PILL_OPTION_LIMIT, PillGroup } from '~/components/common/PillGroup'
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

function isPillOptions(options: readonly PillOptionSpec<string>[]): options is PillOptions<string> {
  return options.length > 0 && options.length <= PILL_OPTION_LIMIT
}

function fixedPillOptions(options: readonly EnumOption[]): PillOptions<string> | undefined {
  const pills = options.map(option => ({ key: option.value, label: option.label }))
  return isPillOptions(pills) ? pills : undefined
}

/**
 * One-of-N choice. A short list renders as PillGroup with radio semantics.
 * A longer list renders as `LoadingMenu`; see the dropdown rule in CLAUDE.md. The
 * branch lives inside a `<Show>` so a change in the option count re-renders
 * rather than being captured once at setup.
 *
 * NEITHER BRANCH REPAIRS THE DOM AFTER A REFUSED WRITE. The `<select>` this
 * replaced had to: its selection lived in `selectedIndex`, so a rejected value
 * stayed on screen until the handler put the old one back by hand. Both
 * branches now re-derive every option from `props.value`, which a refused write
 * leaves untouched.
 *
 * The selected option's `help` renders beneath the control. The backend
 * schema declares one per enum value (each SMTP TLS mode, each captcha
 * provider) and carries it over the wire, but a pill and a menu item can
 * each show a label only, so without this line every declared explanation
 * was discarded. One line under the control serves both branches.
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
            // The trigger's fourth state. Without this, a setting whose value
            // is the empty string -- a fresh install before the first write,
            // or an enum whose schema admits "unset" -- read "No options"
            // above a menu the user can see is populated, because
            // `LoadingMenu` falls back to `emptyLabel` for an empty value.
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
