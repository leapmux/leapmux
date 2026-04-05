import type { Component } from 'solid-js'

interface UsernameFieldProps {
  value: () => string
  onInput: (v: string) => void
  labelClass?: string
}

/** Shared username input field. */
export const UsernameField: Component<UsernameFieldProps> = (props) => {
  return (
    <label class={props.labelClass}>
      Username
      <input
        type="text"
        value={props.value()}
        onInput={e => props.onInput(e.currentTarget.value)}
        autocomplete="username"
      />
    </label>
  )
}
