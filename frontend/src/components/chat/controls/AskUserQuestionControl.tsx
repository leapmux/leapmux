import type { Component } from 'solid-js'
import type { ProviderAskUserQuestion } from '../providers/registry'
import type { ActionsProps, ControlAnswerState, EditorContentRef, Question } from './types'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { createUniqueId, For, Show } from 'solid-js'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { pluralize } from '~/lib/plural'
import { buildAllowResponse, getToolInput } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { pluginFor } from '../providers/registry'
import { CollapsibleList } from './CollapsibleList'
import { ControlActionRow } from './ControlActionRow'

// ---------------------------------------------------------------------------
// Selection helpers
// ---------------------------------------------------------------------------

function preservesSelectionNotes(agentProvider?: AgentProvider): boolean {
  return pluginFor(agentProvider)?.preservesSelectionNotes ?? false
}

function toggleSelection(state: ControlAnswerState, qIdx: number, label: string, multiSelect: boolean, totalQuestions: number, preserveCustomText = false) {
  if (!preserveCustomText) {
    state.setCustomTexts((prev) => {
      if (!(qIdx in prev))
        return prev
      return { ...prev, [qIdx]: '' }
    })
  }
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

function isSelected(state: ControlAnswerState, qIdx: number, label: string) {
  return (state.selections()[qIdx] ?? []).includes(label)
}

/** Check if a question is answered (has selection or non-empty custom text). */
function isPageAnsweredWithOption(state: ControlAnswerState, qIdx: number): boolean {
  const sel = state.selections()[qIdx] ?? []
  if (sel.length > 0)
    return true
  const customText = state.customTexts()[qIdx]?.trim()
  return !!customText
}

export function buildAskAnswers(
  state: ControlAnswerState,
  questions: Question[],
  input: Record<string, unknown>,
  requestId: string,
): Record<string, unknown> {
  const answers: Record<string, string> = {}
  for (let i = 0; i < questions.length; i++) {
    const sel = state.selections()[i] ?? []
    const customText = state.customTexts()[i]?.trim()

    if (sel.length > 0) {
      const key = questions[i].question || questions[i].header || `q${i}`
      answers[key] = sel.join(', ')
    }
    else if (customText) {
      const key = questions[i].question || questions[i].header || `q${i}`
      answers[key] = customText
    }
  }
  const updatedInput = { ...input, answers }
  return buildAllowResponse(requestId, updatedInput)
}

// ---------------------------------------------------------------------------
// controlQuestion (the one AskUserQuestion detection in the app)
// ---------------------------------------------------------------------------

/** The provider capability that owns a request, with the questions it carries. */
export interface ControlQuestion {
  capability: ProviderAskUserQuestion
  questions: Question[]
}

/**
 * Classifies a control request as an AskUserQuestion prompt, or not.
 *
 * This is the ONE place that asks the question. The composer reads it to pick
 * the placeholder and the send path, and the banner reads it to pick the
 * rendered control. A second copy would let the two disagree about one payload.
 *
 * The function is pure, so a caller passes the request INSTANCE it already
 * holds. It never reads the store, and it accepts an absent request, because
 * one caller runs it inside a memo that a removal can re-run.
 */
export function controlQuestion(
  request: ControlRequest | null | undefined,
  agentProvider?: AgentProvider,
): ControlQuestion | undefined {
  if (!request)
    return undefined
  const capability = pluginFor(agentProvider)?.askUserQuestion
  return capability?.isRequest(request.payload)
    ? { capability, questions: capability.extractQuestions(request.payload) }
    : undefined
}

// ---------------------------------------------------------------------------
// trySubmitAskUserQuestion (exported for ChatView)
// ---------------------------------------------------------------------------

/**
 * Save the current editor text into answerState and submit if all questions are
 * answered.  Returns `true` when the response was sent (caller should clear
 * the editor), `false` otherwise.
 *
 * In multi-question mode, when not all questions are answered yet, navigates
 * to the next unanswered question (wrapping around from the end).
 */
export function trySubmitAskUserQuestion(
  state: ControlAnswerState,
  questions: Question[],
  currentContent: string,
  onSubmit: () => void,
  editorContentRef?: EditorContentRef,
  preserveSelectionNotes = false,
): boolean {
  // Save current editor text to the current page.
  const page = state.currentPage()
  state.setCustomTexts(prev => ({ ...prev, [page]: currentContent }))
  if (currentContent && !preserveSelectionNotes) {
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
  onSubmit()
  return true
}

// ---------------------------------------------------------------------------
// Content and Actions components
// ---------------------------------------------------------------------------

/**
 * `questions` may be passed directly when a provider doesn't ship a
 * tool_input shape compatible with `getToolInput` (e.g. Pi's
 * extension_ui_request `select` payloads). Without it the component
 * falls back to extracting `questions` from the wrapped tool input,
 * preserving the original Claude/Codex/OpenCode/Cursor flow.
 */
export const AskUserQuestionContent: Component<{ request: ControlRequest, answerState: ControlAnswerState, optionsDisabled?: boolean, agentProvider?: AgentProvider, questions?: Question[] }> = (props) => {
  const input = () => getToolInput(props.request.payload)
  const questions = () => props.questions ?? (input().questions as Question[] | undefined) ?? []
  const currentPage = () => props.answerState.currentPage()
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
                        <CollapsibleList
                          items={q().options}
                          maxVisible={4}
                          moreLabel={n => `Show ${pluralize(n, 'more option')}\u2026`}
                          renderItem={opt => (
                            <label class={styles.optionItem} data-testid={`question-option-${opt.label}`}>
                              <input
                                type="radio"
                                name={radioName}
                                value={opt.label}
                                checked={(props.answerState.selections()[qIdx()] ?? [])[0] === opt.label}
                                onChange={() => {
                                  toggleSelection(props.answerState, qIdx(), opt.label, false, questions().length, preservesSelectionNotes(props.agentProvider))
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
                        />
                      </fieldset>
                    )
                  })()}
                >
                  <CollapsibleList
                    items={q().options}
                    maxVisible={4}
                    moreLabel={n => `Show ${pluralize(n, 'more option')}\u2026`}
                    renderItem={opt => (
                      <label
                        class={styles.optionItem}
                        data-testid={`question-option-${opt.label}`}
                      >
                        <input
                          type="checkbox"
                          checked={isSelected(props.answerState, qIdx(), opt.label)}
                          onChange={() => toggleSelection(props.answerState, qIdx(), opt.label, true, questions().length, preservesSelectionNotes(props.agentProvider))}
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
                  />
                </Show>
              </div>
            </div>
          )
        }}
      </Show>
    </>
  )
}

export const AskUserQuestionActions: Component<ActionsProps & {
  onSubmitAnswers: () => Promise<void>
  onReject: (message: string) => Promise<void>
}> = (props) => {
  const input = () => getToolInput(props.request.payload)
  const questions = () => props.questions ?? (input().questions as Question[] | undefined) ?? []

  /** Check if question at index is answered, accounting for unsaved editor content on the current page. */
  const isPageAnswered = (qIdx: number) => {
    if (isPageAnsweredWithOption(props.answerState, qIdx))
      return true
    // The current page's editor text hasn't been saved to customTexts yet.
    return qIdx === props.answerState.currentPage() && props.hasEditorContent
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
    const editor = props.editorContentRef?.()
    if (!editor)
      return
    const text = editor.get()
    const page = props.answerState.currentPage()
    props.answerState.setCustomTexts(prev => ({ ...prev, [page]: text }))
    if (text && !preservesSelectionNotes(props.agentProvider)) {
      // Clear selections for this page since custom text overrides for providers
      // that model answers and custom text as mutually exclusive.
      props.answerState.setSelections(prev => ({ ...prev, [page]: [] }))
    }
  }

  /** Restore editor content from customTexts for a given page. */
  const restoreEditorForPage = (page: number) => {
    const editor = props.editorContentRef?.()
    if (!editor)
      return
    const savedText = props.answerState.customTexts()[page] ?? ''
    editor.set(savedText)
  }

  const navigateToPage = (newPage: number) => {
    if (newPage === props.answerState.currentPage())
      return
    saveEditorToCurrentPage()
    props.answerState.setCurrentPage(newPage)
    restoreEditorForPage(newPage)
  }

  const { loading: submitting, start: startSubmitting, stop: stopSubmitting } = createLoadingSignal(apiLoadingTimeoutMs())
  const { loading: stopping, start: startStopping, stop: stopStopping } = createLoadingSignal(apiLoadingTimeoutMs())

  const handleSubmit = async () => {
    startSubmitting()
    saveEditorToCurrentPage()
    try {
      await props.onSubmitAnswers()
    }
    catch {
      stopSubmitting()
    }
  }

  const handleStop = async () => {
    startStopping()
    try {
      await props.onReject('User stopped')
    }
    catch {
      stopStopping()
    }
  }

  const handleYolo = () => {
    const qs = questions()
    for (let i = 0; i < qs.length; i++) {
      if (!isPageAnsweredWithOption(props.answerState, i)) {
        props.answerState.setCustomTexts(prev => ({ ...prev, [i]: 'Go with the recommended option.' }))
      }
    }
    // Auto-submit after filling unanswered questions
    // Need to use setTimeout to let the state settle before reading it
    setTimeout(() => {
      void handleSubmit()
    }, 0)
  }

  return (
    <ControlActionRow
      secondary={(
        <>
          <button
            class="outline"
            onClick={handleStop}
            disabled={stopping()}
            data-testid="control-stop-btn"
          >
            <Show when={stopping()}><Spinner /></Show>
            {stopping() ? 'Stopping...' : 'Stop'}
          </button>
          <Tooltip text="Auto-fill unanswered questions and submit">
            <button
              class="outline"
              onClick={handleYolo}
              disabled={!anyUnanswered()}
              data-testid="control-yolo-btn"
            >
              YOLO
            </button>
          </Tooltip>
        </>
      )}
      centre={(
        <Show when={questions().length > 1}>
          <div class={styles.paginationContainer} data-testid="control-pagination">
            <For each={questions()}>
              {(_, idx) => {
                const isCurrent = () => props.answerState.currentPage() === idx()
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
      )}
      primary={(
        <button
          onClick={handleSubmit}
          disabled={!allAnswered() || submitting()}
          data-testid="control-submit-btn"
        >
          <Show when={submitting()}><Spinner /></Show>
          {submitting() ? 'Submitting...' : 'Submit'}
        </button>
      )}
    />
  )
}
