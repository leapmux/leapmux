import type { Component } from 'solid-js'
import type { AgentStartupPrompt } from '~/generated/proto/leapmux/v1/agent_pb'
import { createSignal, For, Show } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import * as styles from './AgentStartupPromptDialog.css'

export interface AgentStartupPromptDialogProps {
  prompt: AgentStartupPrompt
  onRespond: (proceed: boolean) => Promise<void>
}

export const AgentStartupPromptDialog: Component<AgentStartupPromptDialogProps> = (props) => {
  const [busy, setBusy] = createSignal(false)
  const [dialogAttempt, setDialogAttempt] = createSignal(1)

  const respond = (proceed: boolean) => {
    if (busy())
      return
    setBusy(true)
    void props.onRespond(proceed).catch(() => {
      setBusy(false)
      // A dismiss (Escape, close button, or backdrop click) leaves the inner
      // Dialog in its closing animation state before invoking onClose; re-key
      // so the dialog remounts visibly if the cancel RPC fails.
      if (!proceed)
        setDialogAttempt(n => n + 1)
    })
  }

  return (
    <Show when={dialogAttempt()} keyed>
      {_attempt => (
        <ConfirmDialog
          title={props.prompt.title}
          confirmLabel={props.prompt.confirmLabel || 'Proceed'}
          cancelLabel={props.prompt.cancelLabel || 'Cancel'}
          busy={busy()}
          data-testid="agent-startup-prompt-dialog"
          confirmTestId="agent-startup-prompt-proceed"
          cancelTestId="agent-startup-prompt-cancel"
          onConfirm={() => respond(true)}
          onCancel={() => respond(false)}
        >
          <div class={styles.body}>
            <Show when={props.prompt.description}>
              <p class={styles.description}>{props.prompt.description}</p>
            </Show>
            <Show when={props.prompt.steps.length > 0}>
              <ol class={styles.steps}>
                <For each={props.prompt.steps}>
                  {step => <li>{step}</li>}
                </For>
              </ol>
            </Show>
            <Show when={props.prompt.note}>
              <p class={styles.note}>{props.prompt.note}</p>
            </Show>
          </div>
        </ConfirmDialog>
      )}
    </Show>
  )
}
