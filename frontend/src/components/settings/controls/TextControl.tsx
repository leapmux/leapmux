import type { Component } from 'solid-js'
import { untrack } from 'solid-js'

export interface TextControlProps {
  value: string | undefined
  placeholder?: string
  ariaLabel: string
  /**
   * Commit the typed text. A promise that resolves false means the write
   * was refused, and the control puts the stored value back.
   */
  onChange: (value: string) => void | Promise<boolean | void>
}

/**
 * A free-form single-line text setting.
 *
 * Commits on `change` (blur or Enter), never on `input`. Every commit is one
 * write RPC, so a per-keystroke commit stored each prefix of what the user
 * typed: an admin typing a public base URL had `https://h`, `https://hu`, …
 * accepted and stored in turn, and any mail sent inside that window carried
 * the half-typed URL. The sibling controls that edit free text hold a local
 * draft for the same reason (see SliderControl and SecretControl).
 */
export const TextControl: Component<TextControlProps> = (props) => {
  return (
    <input
      type="text"
      aria-label={props.ariaLabel}
      placeholder={props.placeholder}
      value={props.value ?? ''}
      // A REFUSED write leaves the rejected string in the field. Solid
      // assigns `value` only when the tracked expression CHANGES, and a
      // refused write leaves `props.value` exactly as it was — so without
      // this repair the field keeps showing text the hub never stored, for
      // the life of the dialog. NumberControl repairs a dropped commit the
      // same way.
      onChange={(e) => {
        const el = e.currentTarget
        void Promise.resolve(props.onChange(el.value)).then((ok) => {
          // Read at REPLY time, and deliberately untracked: the value to
          // restore is whatever the binding holds once the write settles,
          // not the one captured when the user left the field.
          if (ok === false)
            el.value = untrack(() => props.value) ?? ''
        })
      }}
    />
  )
}
