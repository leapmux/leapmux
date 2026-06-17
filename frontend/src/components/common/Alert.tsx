import type { JSX } from 'solid-js'
import { Show } from 'solid-js'

/** oat alert variants (see @knadh/oat/css/alert.css). Omitted renders the default info style. */
export type AlertVariant = 'success' | 'warning' | 'error' | 'danger'

export interface AlertProps {
  /** oat `data-variant`; omitted leaves the attribute off so the default (info) style applies. */
  variant?: AlertVariant
  /** Bold lead-in rendered before the body (e.g. a category label). */
  label?: JSX.Element
  /** Alert body. Rendered as escaped JSX children -- never interpolate untrusted text as HTML. */
  children?: JSX.Element
}

/**
 * Inline alert box styled entirely by @knadh/oat's `[role="alert"]` rules
 * (including `data-variant`), so it carries no local CSS. Content is passed as
 * JSX children and is therefore auto-escaped by Solid; this component never uses
 * `innerHTML`, so reminder/tag text containing `<`/`>` renders as literal text.
 */
export function Alert(props: AlertProps): JSX.Element {
  return (
    <div role="alert" data-variant={props.variant}>
      <Show when={props.label != null}>
        <strong>{props.label}</strong>
        {' '}
      </Show>
      {props.children}
    </div>
  )
}
