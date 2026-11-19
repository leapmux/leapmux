import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { ohMyPiControlResponseSummary } from './controlResponse'

const approval = {
  type: 'extension_ui_request',
  id: 'a1',
  method: 'select',
  title: 'Allow tool: bash\nCommand: ls',
  options: ['Approve', 'Deny'],
}

/** The permission prompt that the Worker reads from {@link approval} (`approvalPrompt` in Go). */
const APPROVAL_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'bash',
    command: 'ls',
    options: [
      { optionId: 'Approve', kind: 'allow_once', name: 'Approve' },
      { optionId: 'Deny', kind: 'reject_once', name: 'Deny' },
    ],
  },
}

/** The question prompt that the Worker reads from the bridge's request (`askQuestions` in Go). */
function askPrompt(...questions: Array<{ id: string, question: string }>): ControlPrompt {
  return {
    kind: 'question',
    questions: questions.map(question => ({ ...question, options: [] })),
  }
}

/** A dialog prompt of one variant, as `dialogPrompt` in Go states it. */
function dialogPrompt(variant: 'confirm' | 'input' | 'editor', title: string): ControlPrompt {
  return {
    kind: 'dialog',
    dialog: { title, variant },
  }
}

/** One saved row. `prompt` is what the Worker recorded beside it, or nothing. */
function saved(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'a1',
    claimToken: 't',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** One saved answer to the tool approval. */
function approved(response: Record<string, unknown> | undefined, prompt: ControlPrompt = APPROVAL_PROMPT): PersistedControlResponse {
  return saved(approval, response, prompt)
}

/** omp's answer to a select, an input or an editor dialog, as the Worker saves it. */
function valueResponse(id: string, value: string): Record<string, unknown> {
  return { type: 'extension_ui_response', id, value }
}

/** omp's answer to a confirm dialog, as the Worker saves it. */
function confirmResponse(id: string, confirmed: boolean): Record<string, unknown> {
  return { type: 'extension_ui_response', id, confirmed }
}

/** omp's dismissal of any dialog, as the Worker saves it. */
function cancelResponse(id: string): Record<string, unknown> {
  return { type: 'extension_ui_response', id, cancelled: true }
}

describe('ohMyPiControlResponseSummary', () => {
  // The bridge chooses each option's value, and the reader never sees it. The recorded
  // prompt states the label that each value selects (`askOptions` in Go reads it from
  // the option's markup), so the saved answer reads as the banner drew the choice.
  it('resolves opaque bridge values to the displayed choice labels', () => {
    const request = {
      type: 'leapmux_ask',
      id: 'choice-values',
      questions: [
        {
          id: 'style',
          question: 'Choose a style',
          options: [
            { value: '0', label: 'Beta Fast', markup: '<label><input type="radio"><strong>Beta</strong> <span>Fast</span></label>' },
            { value: '1', label: 'Gamma' },
          ],
        },
      ],
    }
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        {
          id: 'style',
          question: 'Choose a style',
          options: [
            { value: '0', label: 'Beta', description: 'Fast' },
            { value: '1', label: 'Gamma' },
          ],
        },
      ],
    }
    const response = (selected: string[]) => ({
      type: 'leapmux_ask_answer',
      id: 'choice-values',
      answers: [
        { id: 'style', selected },
      ],
    })
    expect(ohMyPiControlResponseSummary(saved(request, response(['0']), prompt))).toEqual({ kind: 'label', text: 'Choose a style: Beta' })
    expect(ohMyPiControlResponseSummary(saved(request, response(['1', '0']), prompt))).toEqual({ kind: 'label', text: 'Choose a style: Gamma, Beta' })
    // A value that the recorded prompt does not state reads as it stands.
    expect(ohMyPiControlResponseSummary(saved(request, response(['7']), prompt))).toEqual({ kind: 'label', text: 'Choose a style: 7' })
    // The native request states only the values, so a row that records no prompt reads them.
    expect(ohMyPiControlResponseSummary(saved(request, response(['0'])))).toEqual({ kind: 'label', text: 'style: 0' })
  })

  it('reads an approval decision', () => {
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Approve')))).toEqual({ kind: 'label', text: 'Allow' })
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Deny')))).toEqual({ kind: 'label', text: 'Deny' })
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Maybe')))).toBeNull()
  })

  // The recorded prompt states the options and their kinds, so the decision reads
  // through it, whatever the stored request offered.
  it('reads an approval decision through the options of the recorded prompt, not the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'permission',
      permission: {
        title: 'bash',
        options: [
          { optionId: 'Run it', kind: 'allow_once', name: 'Run it' },
          { optionId: 'Stop', kind: 'reject_once', name: 'Stop' },
          { optionId: 'Later', kind: 'other', name: 'Later' },
        ],
      },
    }
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Run it'), prompt))).toEqual({ kind: 'label', text: 'Allow' })
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Stop'), prompt))).toEqual({ kind: 'label', text: 'Deny' })
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Later'), prompt))).toBeNull()
    expect(ohMyPiControlResponseSummary(approved(valueResponse('a1', 'Approve'), prompt))).toBeNull()
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads its decision.
  it('reads an approval decision whose native request is gone', () => {
    expect(ohMyPiControlResponseSummary(saved(undefined, valueResponse('a1', 'Approve'), APPROVAL_PROMPT))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // A row with no prompt states no approval, so its value reads as any dialog's value.
  it('reads the value of a row that records no prompt as it stands', () => {
    expect(ohMyPiControlResponseSummary(saved(approval, valueResponse('a1', 'Approve')))).toEqual({ kind: 'label', text: 'Approve' })
  })

  // LeapMux's own Allow and Deny answer a request that omp did not state, through the
  // neutral envelope, and the shared fallback reads it.
  it('leaves the neutral answer to a generic permission to the shared fallback', () => {
    const generic: ControlPrompt = {
      kind: 'permission',
      permission: {
        options: [
          { optionId: 'leapmux:allow', kind: 'allow_once' },
          { optionId: 'leapmux:deny', kind: 'reject_once' },
        ],
      },
    }
    const row = saved({ type: 'unknown' }, {
      type: 'control_response',
      response: {
        request_id: 'a1',
        response: { behavior: 'allow' },
      },
    }, generic)
    expect(ohMyPiControlResponseSummary(row)).toBeNull()
    expect(resolveControlResponseSummary(row, ohMyPiControlResponseSummary)).toEqual({ kind: 'label', text: 'Allow' })
  })

  it('reads a cancellation', () => {
    expect(ohMyPiControlResponseSummary(approved(cancelResponse('a1')))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('reads the bridge\'s answers under their questions', () => {
    const request = {
      type: 'leapmux_ask',
      id: 'q1',
      questions: [
        { id: 'name', question: 'Project name?' },
        { id: 'langs', question: 'Languages?' },
      ],
    }
    const response = {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [
        { id: 'name', custom: 'gamma' },
        {
          id: 'langs',
          selected: ['Go', 'Rust'],
          custom: 'Zig',
        },
      ],
    }
    const prompt = askPrompt({ id: 'name', question: 'Project name?' }, { id: 'langs', question: 'Languages?' })
    expect(ohMyPiControlResponseSummary(saved(request, response, prompt))).toEqual({ kind: 'label', text: 'Project name?: gamma\nLanguages?: Go, Rust; Zig' })
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'leapmux_ask_answer', id: 'q1', answers: [] }))).toEqual({ kind: 'label', text: 'Answered' })
  })

  // The headings come from the prompt that the banner drew, and the prompt lives in the
  // answer row's own metadata, so a row whose request is gone still reads them.
  it('heads the bridge\'s answers with the questions of the recorded prompt, not of the native request', () => {
    const request = {
      type: 'leapmux_ask',
      id: 'q1',
      questions: [
        { id: 'name', question: 'The native question' },
      ],
    }
    const response = {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [
        { id: 'name', custom: 'gamma' },
      ],
    }
    const prompt = askPrompt({ id: 'name', question: 'The recorded question' })
    expect(ohMyPiControlResponseSummary(saved(request, response, prompt))).toEqual({ kind: 'label', text: 'The recorded question: gamma' })
    expect(ohMyPiControlResponseSummary(saved(undefined, response, prompt))).toEqual({ kind: 'label', text: 'The recorded question: gamma' })
    expect(ohMyPiControlResponseSummary(saved(request, response))).toEqual({ kind: 'label', text: 'name: gamma' })
  })

  it('heads an answer with its id when the prompt states no such question, and states the bare answer with no id', () => {
    const request = {
      type: 'leapmux_ask',
      id: 'q1',
      questions: [
        { id: 'name', question: 'Project name?' },
      ],
    }
    const response = {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [
        {
          id: 'color',
          selected: ['blue'],
        },
        {
          selected: ['x'],
          custom: 'y',
        },
      ],
    }
    expect(ohMyPiControlResponseSummary(saved(request, response, askPrompt({ id: 'name', question: 'Project name?' })))).toEqual({ kind: 'label', text: 'color: blue\nx; y' })
  })

  it('leaves out a question the reader left blank, and reads an answer with no words as answered', () => {
    // The bridge's answer states each question's id, the unanswered ones included,
    // and a half that holds only blanks states no answer.
    const request = {
      type: 'leapmux_ask',
      id: 'q1',
      questions: [
        { id: 'name', question: 'Project name?' },
        { id: 'langs', question: 'Languages?' },
      ],
    }
    const partly = {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [
        { id: 'name', selected: [], custom: '  ' },
        {
          id: 'langs',
          selected: ['Go'],
          custom: '',
        },
      ],
    }
    const prompt = askPrompt({ id: 'name', question: 'Project name?' }, { id: 'langs', question: 'Languages?' })
    expect(ohMyPiControlResponseSummary(saved(request, partly, prompt))).toEqual({ kind: 'label', text: 'Languages?: Go' })
    const blank = {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [
        { id: 'name' },
        { id: 'langs', selected: [], custom: '' },
      ],
    }
    expect(ohMyPiControlResponseSummary(saved(request, blank, prompt))).toEqual({ kind: 'label', text: 'Answered' })
  })

  it('reads the bridge\'s answers when the answer list is not a list or holds no records', () => {
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'leapmux_ask_answer', id: 'q1', answers: 'gamma' }))).toEqual({ kind: 'label', text: 'Answered' })
    expect(ohMyPiControlResponseSummary(saved(undefined, {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: ['gamma', null],
    }))).toEqual({ kind: 'label', text: 'Answered' })
  })

  it('reads a cancellation before any other field the response states', () => {
    // omp's dismissal carries `cancelled` alone; a response that also states a value
    // is still a dismissal.
    expect(ohMyPiControlResponseSummary(approved({
      ...valueResponse('a1', 'Approve'),
      cancelled: true,
    }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(ohMyPiControlResponseSummary(saved(undefined, {
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [],
      cancelled: true,
    }))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('reads a response with `cancelled: false` by its other fields', () => {
    const input = {
      type: 'extension_ui_request',
      id: 'i1',
      method: 'input',
      title: 'Name',
    }
    expect(ohMyPiControlResponseSummary(saved(input, {
      ...valueResponse('i1', 'main'),
      cancelled: false,
    }, dialogPrompt('input', 'Name')))).toEqual({ kind: 'label', text: 'main' })
  })

  it('reads a confirm and a value', () => {
    const confirm = {
      type: 'extension_ui_request',
      id: 'c1',
      method: 'confirm',
      title: 'Proceed?',
    }
    expect(ohMyPiControlResponseSummary(saved(confirm, confirmResponse('c1', true), dialogPrompt('confirm', 'Proceed?')))).toEqual({ kind: 'label', text: 'Confirmed' })
    expect(ohMyPiControlResponseSummary(saved(confirm, confirmResponse('c1', false), dialogPrompt('confirm', 'Proceed?')))).toEqual({ kind: 'label', text: 'Declined' })
    const input = {
      type: 'extension_ui_request',
      id: 'i1',
      method: 'input',
      title: 'Name',
    }
    expect(ohMyPiControlResponseSummary(saved(input, valueResponse('i1', 'main'), dialogPrompt('input', 'Name')))).toEqual({ kind: 'label', text: 'main' })
    expect(ohMyPiControlResponseSummary(saved(input, valueResponse('i1', ''), dialogPrompt('input', 'Name')))).toEqual({ kind: 'label', text: 'Empty answer' })
  })

  it('answers null for a response it cannot read', () => {
    expect(ohMyPiControlResponseSummary(approved(undefined))).toBeNull()
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'something' }))).toBeNull()
    // A value or a confirmation of the wrong type states no answer.
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'extension_ui_response', id: 'i1', value: 7 }))).toBeNull()
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'extension_ui_response', id: 'c1', confirmed: 'yes' }))).toBeNull()
    // An approval answered with a confirmation is no decision the approval offers.
    expect(ohMyPiControlResponseSummary(approved(confirmResponse('a1', true)))).toBeNull()
  })
})
