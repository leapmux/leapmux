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

  const respond = (proceed: boolean): Promise<boolean> => {
    if (busy())
      return Promise.resolve(false)
    setBusy(true)
    return props.onRespond(proceed).then(
      () => true,
      () => {
        setBusy(false)
        return false
      },
    )
  }

  return (
    <ConfirmDialog
      title={props.prompt.title}
      confirmLabel={props.prompt.confirmLabel || 'Proceed'}
      cancelLabel={props.prompt.cancelLabel || 'Cancel'}
      busy={busy()}
      data-testid="agent-startup-prompt-dialog"
      confirmTestId="agent-startup-prompt-proceed"
      cancelTestId="agent-startup-prompt-cancel"
      onConfirm={() => {
        void respond(true)
      }}
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
  )
}
