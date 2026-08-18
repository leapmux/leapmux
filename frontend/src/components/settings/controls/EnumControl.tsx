import type { Component, JSX } from 'solid-js'
import { For, Show, untrack } from 'solid-js'
import { PillGroup } from '~/components/common/PillGroup'
import * as styles from '../SettingRow.css'

export interface EnumOption {
  value: string
  label: string
  help?: string
}

export interface EnumControlProps {
  /** Accessible name for the group / label for the select. */
  ariaLabel: string
  value: string
  options: EnumOption[]
  /**
   * Commit the chosen value. A promise that resolves false means the write
   * was refused, and the `<select>` branch puts the stored value back.
   */
  onChange: (value: string) => void | Promise<boolean | void>
}

/** Enums with at most this many options render as radio pills. */
const PILL_MAX_OPTIONS = 4

/**
 * One-of-N choice. Few options render as the promoted PillGroup (radiogroup
 * semantics); more than PILL_MAX_OPTIONS would overflow a row of pills, so
 * they render as a native `<select>` styled like every other select in the
 * app (see LoadingSelect). The branch lives inside a `<Show>` so a change in
 * the option count re-renders rather than being captured once at setup.
 *
 * The selected option's `help` renders beneath the control. The backend
 * schema declares one per enum value (each SMTP TLS mode, each captcha
 * provider) and carries it over the wire, but a pill and an `<option>` can
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
        when={props.options.length <= PILL_MAX_OPTIONS}
        fallback={(
          <select
            aria-label={props.ariaLabel}
            value={props.value}
            // A REFUSED write leaves the rejected option showing. Solid
            // assigns `value` only when the tracked expression CHANGES,
            // and a refused write leaves `props.value` exactly as it was.
            // The pill branch needs no repair: it re-derives every pill
            // from `props.value` through `selected`.
            onChange={(e) => {
              const el = e.currentTarget
              void Promise.resolve(props.onChange(el.value)).then((ok) => {
                // Read at REPLY time, and deliberately untracked: the value
                // to restore is whatever the binding holds once the write
                // settles.
                if (ok === false)
                  el.value = untrack(() => props.value)
              })
            }}
          >
            <For each={props.options}>
              {opt => <option value={opt.value}>{opt.label}</option>}
            </For>
          </select>
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
