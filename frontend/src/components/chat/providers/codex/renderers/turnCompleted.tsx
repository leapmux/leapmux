import type { CodexMessageRenderer } from '../defineRenderer'
import { Show } from 'solid-js'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_STATUS } from '~/types/toolMessages'
import { resultDivider } from '../../../messageStyles.css'

function FailureLabel(props: { message: string, details: string }) {
  return (
    <div class={resultDivider} style={{ color: 'var(--danger)' }}>
      {props.details ? `${props.message} — ${props.details}` : props.message}
    </div>
  )
}

/** Renders Codex turn/completed as a result divider. */
export const CodexTurnCompletedRenderer: CodexMessageRenderer = (props) => {
  const turn = (): Record<string, unknown> | null => pickObject(isObject(props.parsed) ? props.parsed : null, 'turn')
  const status = (): string => pickString(turn(), 'status')
  const failure = (): { message: string, details: string } | null => {
    const t = turn()
    if (!t || pickString(t, 'status') !== CODEX_STATUS.FAILED || !isObject(t.error))
      return null
    const error = t.error as Record<string, unknown>
    return {
      message: pickString(error, 'message', 'Unknown error'),
      details: pickString(error, 'additionalDetails'),
    }
  }

  return (
    <Show when={status()}>
      <Show
        when={failure()}
        fallback={<div class={resultDivider}>{`Turn ${status()}`}</div>}
      >
        {fail => <FailureLabel message={fail().message} details={fail().details} />}
      </Show>
    </Show>
  )
}
