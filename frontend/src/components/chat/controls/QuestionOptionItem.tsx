import type { Component } from 'solid-js'
import type { QuestionOption } from './types'
import { Show } from 'solid-js'
import * as styles from '../ControlRequestBanner.css'
import { MarkdownText } from '../messageRenderers'
import { questionOption, questionPreview } from './QuestionOptionItem.css'
import { questionOptionValue } from './types'

/** Keep preview text outside the label so text selection cannot select an answer. */
export const QuestionOptionItem: Component<{
  option: QuestionOption
  type: 'radio' | 'checkbox'
  name?: string
  checked: boolean
  disabled?: boolean
  onChange: () => void
}> = (props) => {
  const preview = () => typeof props.option.preview === 'string' && props.option.preview.trim() ? props.option.preview : undefined
  return (
    <div class={questionOption}>
      <label class={styles.optionItem} data-testid={`question-option-${props.option.label}`}>
        <input type={props.type} name={props.name} value={questionOptionValue(props.option)} checked={props.checked} onChange={() => props.onChange()} disabled={props.disabled} />
        <span class={styles.optionContent}>
          <span class={styles.optionLabel}>{props.option.label}</span>
          <Show when={props.option.description}>
            <span class={styles.optionDescription}>{props.option.description}</span>
          </Show>
        </span>
      </label>
      <Show when={preview()}>
        {text => (
          <div class={questionPreview} data-question-preview role="region" aria-label={`${props.option.label} preview`} tabIndex={0}>
            <MarkdownText text={text()} />
          </div>
        )}
      </Show>
    </div>
  )
}
