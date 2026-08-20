import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { PillGroup } from '~/components/common/PillGroup'
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

/** Enums with at most this many options render as radio pills. */
const PILL_MAX_OPTIONS = 4

/**
 * One-of-N choice. Few options render as the promoted PillGroup (radiogroup
 * semantics); more than PILL_MAX_OPTIONS would overflow a row of pills, so
 * they render as a `LoadingMenu` -- see the dropdown rule in CLAUDE.md. The
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

  const pills = (): JSX.Element => (
    <PillGroup
      label={props.ariaLabel}
      options={props.options}
      selected={v => v === props.value}
      onSelect={props.onChange}
    />
  )
  return (
    <>
      <Show
        // An EMPTY list takes the menu branch, although zero is fewer than
        // PILL_MAX_OPTIONS. `PillGroup` has nothing to render for it and drew a
        // blank row where the control belongs, while `LoadingMenu` answers for
        // exactly this state with a disabled trigger that says so -- which is
        // also what makes the required `emptyLabel` below reachable at all.
        // `LoadingMenu` derives the empty state from the options it is given,
        // so this branch and that one cannot disagree about which it is.
        when={props.options.length > 0 && props.options.length <= PILL_MAX_OPTIONS}
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
        {pills()}
      </Show>
      <Show when={selectedHelp()}>
        {help => <div class={styles.helpText}>{help()}</div>}
      </Show>
    </>
  )
}
