import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { ConfirmButton } from './ConfirmButton'
import { Dialog } from './Dialog'

interface SecondaryAction {
  label: string
  testId?: string
  onClick: () => void
  /** Wrap in ConfirmButton (two-click arming) when true. */
  danger?: boolean
}

interface ConfirmDialogProps {
  'title': string
  'children': JSX.Element
  'confirmLabel'?: string
  'cancelLabel'?: string
  /** Apply danger styling + ConfirmButton arming to the PRIMARY action. */
  'danger'?: boolean
  'busy'?: boolean
  'onConfirm': () => void
  'onCancel': () => void
  'data-testid'?: string
  'confirmTestId'?: string
  'cancelTestId'?: string
  /**
   * Optional third action between Cancel and the primary (e.g. "Close all
   * tabs" in the close-grid/tile/window flow). When `danger` is true the
   * label is wrapped in a ConfirmButton so the user has to click twice.
   */
  'secondary'?: SecondaryAction
}

export const ConfirmDialog: Component<ConfirmDialogProps> = (props) => {
  // Form-level submit so non-danger primaries fire on Enter. The danger
  // branch keeps the ConfirmButton's two-click arming and renders a
  // type="button" instead, so Enter doesn't bypass it.
  const handleSubmit = (e: SubmitEvent) => {
    e.preventDefault()
    if (!props.busy && !props.danger)
      props.onConfirm()
  }

  return (
    <Dialog
      title={props.title}
      busy={props.busy}
      onClose={() => props.onCancel()}
      data-testid={props['data-testid']}
    >
      <form onSubmit={handleSubmit}>
        <section>{props.children}</section>
        <footer>
          <button
            type="button"
            class="outline"
            disabled={props.busy}
            data-testid={props.cancelTestId}
            onClick={() => props.onCancel()}
          >
            {props.cancelLabel ?? 'Cancel'}
          </button>
          <Show when={props.secondary}>
            {secondary => (
              <Show
                when={secondary().danger}
                fallback={(
                  <button
                    type="button"
                    disabled={props.busy}
                    data-testid={secondary().testId}
                    onClick={() => secondary().onClick()}
                  >
                    {secondary().label}
                  </button>
                )}
              >
                <ConfirmButton
                  data-variant="danger"
                  disabled={props.busy}
                  data-testid={secondary().testId}
                  onClick={() => secondary().onClick()}
                >
                  {secondary().label}
                </ConfirmButton>
              </Show>
            )}
          </Show>
          <Show
            when={props.danger}
            fallback={(
              <button type="submit" disabled={props.busy} data-testid={props.confirmTestId}>
                {props.confirmLabel ?? 'OK'}
              </button>
            )}
          >
            <ConfirmButton
              data-variant="danger"
              disabled={props.busy}
              data-testid={props.confirmTestId}
              onClick={() => props.onConfirm()}
            >
              {props.confirmLabel ?? 'OK'}
            </ConfirmButton>
          </Show>
        </footer>
      </form>
    </Dialog>
  )
}
