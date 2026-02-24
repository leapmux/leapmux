import type { Component } from 'solid-js'
import type { ActionsProps, AskQuestionState, EditorContentRef, Question } from './types'
import type { ControlRequest } from '~/stores/control.store'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createUniqueId, For, Show } from 'solid-js'
import { agentLoadingTimeoutMs, apiLoadingTimeoutMs } from '~/api/transport'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { spinner } from '~/styles/animations.css'
import { buildAllowResponse, buildDenyResponse, getToolInput } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { sendResponse } from './types'

// ---------------------------------------------------------------------------
// Selection helpers
// ---------------------------------------------------------------------------

function toggleSelection(state: AskQuestionState, qIdx: number, label: string, multiSelect: boolean, totalQuestions: number) {
  state.setSelections((prev) => {
    const current = prev[qIdx] ?? []
    if (multiSelect) {
      const newSel = current.includes(label)
        ? current.filter(l => l !== label)
        : [...current, label]
      return { ...prev, [qIdx]: newSel }
    }
    return { ...prev, [qIdx]: [label] }
  })
  // Auto-advance to next page on single-select option click (multi-question only)
  if (!multiSelect && totalQuestions > 1) {
    const nextPage = state.currentPage() + 1
    if (nextPage < totalQuestions) {
      state.setCurrentPage(nextPage)
    }
  }
}

function isSelected(state: AskQuestionState, qIdx: number, label: string) {
  return (state.selections()[qIdx] ?? []).includes(label)
}

/** Check if a question is answered (has selection or non-empty custom text). */
function isPageAnsweredWithOption(state: AskQuestionState, qIdx: number): boolean {
  const sel = state.selections()[qIdx] ?? []
  if (sel.length > 0)
    return true
  const customText = state.customTexts()[qIdx]?.trim()
  return !!customText
}

function buildAskAnswers(
  state: AskQuestionState,
  questions: Question[],
  input: Record<string, unknown>,
  requestId: string,
): Record<string, unknown> {
  const answers: Record<string, string> = {}
  for (let i = 0; i < questions.length; i++) {
    const sel = state.selections()[i] ?? []
    const customText = state.customTexts()[i]?.trim()

    if (sel.length > 0) {
      const key = questions[i].header || `q${i}`
      answers[key] = sel.join(', ')
    }
    else if (customText) {
      const key = questions[i].header || `q${i}`
      answers[key] = customText
    }
  }
  const updatedInput = { ...input, answers }
  return buildAllowResponse(requestId, updatedInput)
}

// ---------------------------------------------------------------------------
// trySubmitAskUserQuestion (exported for ChatView)
// ---------------------------------------------------------------------------

/**
 * Save the current editor text into askState and submit if all questions are
 * answered.  Returns `true` when the response was sent (caller should clear
 * the editor), `false` otherwise.
 *
 * In multi-question mode, when not all questions are answered yet, navigates
 * to the next unanswered question (wrapping around from the end).
 */
export function trySubmitAskUserQuestion(
  state: AskQuestionState,
  request: ControlRequest,
  currentContent: string,
  onRespond: (agentId: string, content: Uint8Array) => void,
  editorContentRef?: EditorContentRef,
): boolean {
  const input = getToolInput(request.payload)
  const questions = (input.questions as Question[] | undefined) ?? []

  // Save current editor text to the current page.
  const page = state.currentPage()
  if (currentContent) {
    state.setCustomTexts(prev => ({ ...prev, [page]: currentContent }))
    state.setSelections(prev => ({ ...prev, [page]: [] }))
  }

  // Check if every question is now answered.
  let allAnswered = true
  for (let i = 0; i < questions.length; i++) {
    if (!isPageAnsweredWithOption(state, i)) {
      allAnswered = false
      break
    }
  }

  if (!allAnswered && questions.length > 1) {
    // Navigate to the next unanswered question with wrap-around.
    for (let offset = 1; offset < questions.length; offset++) {
      const idx = (page + offset) % questions.length
      if (!isPageAnsweredWithOption(state, idx)) {
        state.setCurrentPage(idx)
        editorContentRef?.set(state.customTexts()[idx] ?? '')
        break
      }
    }
    return false
  }

  if (!allAnswered)
    return false

  // Build and send the response.
  const response = buildAskAnswers(state, questions, input, request.requestId)
  const bytes = new TextEncoder().encode(JSON.stringify(response))
  onRespond(request.agentId, bytes)
  return true
}

// ---------------------------------------------------------------------------
// Content and Actions components
// ---------------------------------------------------------------------------

export const AskUserQuestionContent: Component<{ request: ControlRequest, askState: AskQuestionState, optionsDisabled?: boolean }> = (props) => {
  const input = () => getToolInput(props.request.payload)
  const questions = () => (input().questions as Question[] | undefined) ?? []
  const currentPage = () => props.askState.currentPage()
  const currentQuestion = () => questions()[currentPage()]

  return (
    <>
      <div class={styles.controlBannerTitle}>Agent Question</div>
      <Show when={questions().length > 1}>
        <div class={styles.questionPageHeader}>
          Question
          {' '}
          {currentPage() + 1}
          {' '}
          of
          {' '}
          {questions().length}
        </div>
      </Show>
      <Show when={currentQuestion()}>
        {(q) => {
          const qIdx = currentPage
          return (
            <div class={styles.questionGroup} data-testid="control-question-group">
              <div class={styles.questionLabel}>{q().question}</div>
              <div class={styles.optionList} style={props.optionsDisabled ? { 'opacity': '0.5', 'pointer-events': 'none' } : undefined}>
                <Show
                  when={q().multiSelect}
                  fallback={(() => {
                    const radioName = createUniqueId()
                    return (
                      <fieldset>
                        <For each={q().options}>
                          {opt => (
                            <label class={styles.optionItem} data-testid={`question-option-${opt.label}`}>
                              <input
                                type="radio"
                                name={radioName}
                                value={opt.label}
                                checked={(props.askState.selections()[qIdx()] ?? [])[0] === opt.label}
                                onChange={() => {
                                  toggleSelection(props.askState, qIdx(), opt.label, false, questions().length)
                                }}
                                disabled={props.optionsDisabled}
                              />
                              <span class={styles.optionContent}>
                                <span class={styles.optionLabel}>{opt.label}</span>
                                <Show when={opt.description}>
                                  <span class={styles.optionDescription}>{opt.description}</span>
                                </Show>
                              </span>
                            </label>
                          )}
                        </For>
                      </fieldset>
                    )
                  })()}
                >
                  <For each={q().options}>
                    {opt => (
                      <label
                        class={styles.optionItem}
                        data-testid={`question-option-${opt.label}`}
                      >
                        <input
                          type="checkbox"
                          checked={isSelected(props.askState, qIdx(), opt.label)}
                          onChange={() => toggleSelection(props.askState, qIdx(), opt.label, true, questions().length)}
                          disabled={props.optionsDisabled}
                        />
                        <span class={styles.optionContent}>
                          <span class={styles.optionLabel}>{opt.label}</span>
                          <Show when={opt.description}>
                            <span class={styles.optionDescription}>{opt.description}</span>
                          </Show>
                        </span>
                      </label>
                    )}
                  </For>
                </Show>
              </div>
            </div>
          )
        }}
      </Show>
    </>
  )
}

export const AskUserQuestionActions: Component<ActionsProps> = (props) => {
  const input = () => getToolInput(props.request.payload)
  const questions = () => (input().questions as Question[] | undefined) ?? []

  /** Check if question at index is answered, accounting for unsaved editor content on the current page. */
  const isPageAnswered = (qIdx: number) => {
    if (isPageAnsweredWithOption(props.askState, qIdx))
      return true
    // The current page's editor text hasn't been saved to customTexts yet.
    return qIdx === props.askState.currentPage() && props.hasEditorContent
  }

  const allAnswered = () => {
    const qs = questions()
    for (let i = 0; i < qs.length; i++) {
      if (!isPageAnswered(i))
        return false
    }
    return qs.length > 0
  }

  const anyUnanswered = () => {
    const qs = questions()
    for (let i = 0; i < qs.length; i++) {
      if (!isPageAnswered(i))
        return true
    }
    return false
  }

  /** Save current editor text to customTexts for the current page (if non-empty). */
  const saveEditorToCurrentPage = () => {
    if (!props.editorContentRef)
      return
    const text = props.editorContentRef.get()
    if (text) {
      const page = props.askState.currentPage()
      props.askState.setCustomTexts(prev => ({ ...prev, [page]: text }))
      // Clear selections for this page since custom text overrides
      props.askState.setSelections(prev => ({ ...prev, [page]: [] }))
    }
  }

  /** Restore editor content from customTexts for a given page. */
  const restoreEditorForPage = (page: number) => {
    if (!props.editorContentRef)
      return
    const savedText = props.askState.customTexts()[page] ?? ''
    props.editorContentRef.set(savedText)
  }

  const navigateToPage = (newPage: number) => {
    if (newPage === props.askState.currentPage())
      return
    saveEditorToCurrentPage()
    props.askState.setCurrentPage(newPage)
    restoreEditorForPage(newPage)
  }

  const { loading: submitting, start: startSubmitting, stop: stopSubmitting } = createLoadingSignal(agentLoadingTimeoutMs(true))
  const { loading: stopping, start: startStopping, stop: stopStopping } = createLoadingSignal(apiLoadingTimeoutMs())

  const handleSubmit = async () => {
    startSubmitting()
    saveEditorToCurrentPage()
    try {
      await sendResponse(props.request.agentId, props.onRespond, buildAskAnswers(props.askState, questions(), input(), props.request.requestId))
    }
    catch {
      stopSubmitting()
    }
  }

  const handleStop = async () => {
    startStopping()
    try {
      await sendResponse(props.request.agentId, props.onRespond, buildDenyResponse(props.request.requestId, 'User stopped'))
    }
    catch {
      stopStopping()
    }
  }

  const handleYolo = () => {
    const qs = questions()
    for (let i = 0; i < qs.length; i++) {
      if (!isPageAnsweredWithOption(props.askState, i)) {
        props.askState.setCustomTexts(prev => ({ ...prev, [i]: 'Go with the recommended option.' }))
      }
    }
    // Auto-submit after filling unanswered questions
    // Need to use setTimeout to let the state settle before reading it
    setTimeout(() => {
      const response = buildAskAnswers(props.askState, questions(), input(), props.request.requestId)
      sendResponse(props.request.agentId, props.onRespond, response)
    }, 0)
  }

  return (
    <div class={styles.controlFooter} data-testid="control-footer">
      <div class={styles.controlFooterLeft}>
        <button
          class="outline"
          onClick={handleStop}
          disabled={stopping()}
          data-testid="control-stop-btn"
        >
          <Show when={stopping()}><LoaderCircle size={14} class={spinner} /></Show>
          {stopping() ? 'Stopping...' : 'Stop'}
        </button>
        <button
          class="outline"
          onClick={handleYolo}
          disabled={!anyUnanswered()}
          data-testid="control-yolo-btn"
          title="Auto-fill unanswered questions and submit"
        >
          YOLO
        </button>
        {props.infoTrigger}
      </div>
      <Show when={questions().length > 1}>
        <div class={styles.paginationContainer} data-testid="control-pagination">
          <For each={questions()}>
            {(_, idx) => {
              const isCurrent = () => props.askState.currentPage() === idx()
              const answered = () => isPageAnswered(idx())
              return (
                <button
                  type="button"
                  class={`${styles.paginationItem} ${isCurrent() ? styles.paginationItemCurrent : ''} ${answered() ? styles.paginationItemAnswered : ''}`}
                  onClick={() => navigateToPage(idx())}
                >
                  {idx() + 1}
                </button>
              )
            }}
          </For>
        </div>
      </Show>
      <div class={styles.controlFooterRight}>
        <button
          onClick={handleSubmit}
          disabled={!allAnswered() || submitting()}
          data-testid="control-submit-btn"
        >
          <Show when={submitting()}><LoaderCircle size={14} class={spinner} /></Show>
          {submitting() ? 'Submitting...' : 'Submit'}
        </button>
      </div>
    </div>
  )
}
