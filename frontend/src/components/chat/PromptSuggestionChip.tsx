import type { Component } from 'solid-js'
import type { PromptSuggestion } from '~/stores/chatPromptSuggestion'
import X from 'lucide-solid/icons/x'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { getEditorRef, insertIntoAgentEditor } from '~/stores/editorRef.store'
import * as styles from './PromptSuggestionChip.css'

export interface PromptSuggestionChipProps {
  agentId: string
  suggestion?: PromptSuggestion | undefined
  editorEmpty: boolean
  idle: boolean
  onDismiss: () => void
}

/** Insert a suggested next message into an empty composer without sending it. */
export const PromptSuggestionChip: Component<PromptSuggestionChipProps> = (props) => {
  const visibleSuggestion = () => props.agentId && props.idle && props.editorEmpty && props.suggestion?.text.trim()
    ? props.suggestion
    : undefined
  const useSuggestion = () => {
    const suggestion = visibleSuggestion()
    const editor = getEditorRef(props.agentId)
    // The editor's visible empty state can lag behind its live document.
    if (!suggestion || !editor || editor.get() !== '')
      return
    if (insertIntoAgentEditor(props.agentId, suggestion.text) !== 'refused')
      props.onDismiss()
  }
  return (
    <Show when={visibleSuggestion()}>
      {suggestion => (
        <div class={styles.chip} data-testid="prompt-suggestion-chip">
          <div class={styles.copy}>
            <div class={styles.label}>Suggested next message</div>
            <div class={styles.text}>{suggestion().text}</div>
          </div>
          <button type="button" class={styles.action} data-testid="prompt-suggestion-use" onClick={useSuggestion}>Use</button>
          <Tooltip text="Dismiss suggestion" ariaLabel>
            <button type="button" class={styles.dismiss} data-testid="prompt-suggestion-dismiss" onClick={() => props.onDismiss()}>
              <Icon icon={X} size="xs" />
            </button>
          </Tooltip>
        </div>
      )}
    </Show>
  )
}
