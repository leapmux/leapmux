import type { ControlPrompt, PermissionOption } from './model/controlPrompt'
import type { ControlQuestion } from './model/question'
import type { ControlDecision, DialogAction as DialogActionValue, QuestionAnswer } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ControlDecisionSchema, DialogAction, ElicitationAction, QuestionAnswerSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { ALLOW_DENY_OPTIONS } from '~/test-support/controlRequests'
import { controlDecisionSummary } from './controlDecisionSummary'

/**
 * The words of one saved answer, with the labels of the agent's permission modes that
 * the browser holds. A mode with no label here reads as unknown, as it does for an
 * agent that the browser holds no settings of.
 */
function summary(prompt: ControlPrompt, decision: ControlDecision, modeLabels: Record<string, string> = {}) {
  return controlDecisionSummary(prompt, decision, mode => Object.hasOwn(modeLabels, mode)
    ? modeLabels[mode]
    : undefined)
}

function label(text: string) {
  return { kind: 'label', text }
}

function feedback(message: string) {
  return { kind: 'feedback', message }
}

function permissionPrompt(options: PermissionOption[]): ControlPrompt {
  return {
    kind: 'permission',
    permission: { title: 'Run', options },
  }
}

function permission(optionId: string, feedbackText = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'permission',
      value: { optionId, feedback: feedbackText },
    },
  })
}

function plan(value: { approve?: boolean, choiceId?: string, feedback?: string, permissionMode?: string }): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: { case: 'plan', value },
  })
}

function questions(answers: Array<Partial<Pick<QuestionAnswer, 'selected' | 'customText'>>>): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'question',
      value: {
        answers: answers.map(answer => create(QuestionAnswerSchema, answer)),
      },
    },
  })
}

function cancelledQuestions(feedbackText = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'question',
      value: { cancelled: true, feedback: feedbackText },
    },
  })
}

function elicitation(action: ElicitationAction, contentJson = '', acceptChoiceKey = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'elicitation',
      value: { action, contentJson, acceptChoiceKey },
    },
  })
}

function dialog(action: DialogActionValue, value = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'dialog',
      value: { action, value },
    },
  })
}

describe('controlDecisionSummary', () => {
  describe('a permission', () => {
    const named: PermissionOption[] = [
      { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
      {
        optionId: 'session',
        kind: 'allow_always',
        name: 'Allow for this session',
        scope: 'session',
      },
      { optionId: 'no', kind: 'reject_once', name: 'No, and tell the agent what to do' },
    ]

    it('reads the name of the chosen option', () => {
      expect(summary(permissionPrompt(named), permission('once'))).toEqual(label('Allow once'))
      expect(summary(permissionPrompt(named), permission('session'))).toEqual(label('Allow for this session'))
      expect(summary(permissionPrompt(named), permission('no'))).toEqual(label('No, and tell the agent what to do'))
    })

    // A runtime that states no option gets LeapMux's own allow and deny, which carry no
    // name: the saved row reads the words of their buttons.
    it('reads LeapMux\'s own nameless options as the words of their buttons', () => {
      const prompt = permissionPrompt([...ALLOW_DENY_OPTIONS])
      expect(summary(prompt, permission(ALLOW_DENY_OPTIONS[0]!.optionId))).toEqual(label('Allow'))
      expect(summary(prompt, permission(ALLOW_DENY_OPTIONS[1]!.optionId))).toEqual(label('Deny'))
    })

    it('reads a nameless refusal that ends the turn as Cancel', () => {
      const prompt = permissionPrompt([
        { optionId: 'stop', kind: 'reject_once', endsTurn: true },
      ])
      expect(summary(prompt, permission('stop'))).toEqual(label('Cancel'))
    })

    // Goose and Oh My Pi name each option by its own id, which is no label: a one-time
    // answer reads the words of its button, and a remembered one the label of its kind.
    it('reads an option named by its id as the words of its button', () => {
      const prompt = permissionPrompt([
        { optionId: 'allow_once', kind: 'allow_once', name: 'allow_once' },
        { optionId: 'allow_always', kind: 'allow_always', name: 'allow_always' },
        { optionId: 'reject_once', kind: 'reject_once', name: 'reject_once' },
        { optionId: 'reject_always', kind: 'reject_always' },
      ])
      expect(summary(prompt, permission('allow_once'))).toEqual(label('Allow'))
      expect(summary(prompt, permission('allow_always'))).toEqual(label('Allow always'))
      expect(summary(prompt, permission('reject_once'))).toEqual(label('Deny'))
      expect(summary(prompt, permission('reject_always'))).toEqual(label('Reject always'))
      const words = permissionPrompt([
        { optionId: 'Approve', kind: 'allow_once', name: 'Approve' },
        { optionId: 'Deny', kind: 'reject_once', name: 'Deny' },
      ])
      expect(summary(words, permission('Approve'))).toEqual(label('Allow'))
      expect(summary(words, permission('Deny'))).toEqual(label('Deny'))
    })

    it('reads an option of another kind by its name, else by its id', () => {
      const prompt = permissionPrompt([
        { optionId: 'escalate', kind: 'other', name: 'Ask an admin' },
        { optionId: 'modify', kind: 'other' },
      ])
      expect(summary(prompt, permission('escalate'))).toEqual(label('Ask an admin'))
      expect(summary(prompt, permission('modify'))).toEqual(label('modify'))
    })

    it('reads the reason of a refusal as its feedback, trimmed', () => {
      expect(summary(permissionPrompt(named), permission('no', '  Use the other file.\n')))
        .toEqual(feedback('Use the other file.'))
    })

    // The Worker leaves out the feedback that it delivers as a message of its own, and
    // white space alone is no reason.
    it('reads a refusal with no reason as its option', () => {
      expect(summary(permissionPrompt(named), permission('no', ' \t\n'))).toEqual(label('No, and tell the agent what to do'))
    })

    it('reads nothing for an option that the prompt does not offer', () => {
      expect(summary(permissionPrompt(named), permission('always'))).toBeNull()
      expect(summary(permissionPrompt([]), permission(''))).toBeNull()
    })

    // An option id that spells an Object.prototype member must not reach one.
    it('reads nothing for an option id that names an object member', () => {
      expect(summary(permissionPrompt(named), permission('toString'))).toBeNull()
    })
  })

  describe('a plan', () => {
    const prompt: ControlPrompt = {
      kind: 'plan',
      choices: [
        { id: 'fast', label: 'Approve: the fast path', effect: 'approve' },
        { id: 'revise', label: 'Request revisions', effect: 'refuse' },
        { id: 'later', label: 'Save for later', effect: 'other' },
      ],
    }

    it('reads a plain approval and a plain refusal as the words of their buttons', () => {
      expect(summary({ kind: 'plan' }, plan({ approve: true }))).toEqual(label('Approve'))
      expect(summary({ kind: 'plan' }, plan({ approve: false }))).toEqual(label('Reject'))
    })

    it('reads what to change in a refusal as its feedback', () => {
      expect(summary({ kind: 'plan' }, plan({ feedback: ' Split step 2. ' }))).toEqual(feedback('Split step 2.'))
    })

    it('reads the label of the chosen choice', () => {
      expect(summary(prompt, plan({ approve: true, choiceId: 'fast' }))).toEqual(label('Approve: the fast path'))
      expect(summary(prompt, plan({ choiceId: 'revise' }))).toEqual(label('Request revisions'))
      expect(summary(prompt, plan({ choiceId: 'later' }))).toEqual(label('Save for later'))
    })

    it('reads the feedback of a refusing choice', () => {
      expect(summary(prompt, plan({ choiceId: 'revise', feedback: 'Keep the tests.' }))).toEqual(feedback('Keep the tests.'))
    })

    // The mode that an approval switches to changes what the agent does next, so the
    // row names it, in the agent's own words for the mode.
    it('reads an approval that switches the permission mode with the label of the mode', () => {
      const labels = { 'act': 'Act', 'auto-medium': 'Auto (Medium)' }
      expect(summary({ kind: 'plan' }, plan({ approve: true, permissionMode: 'act' }), labels)).toEqual(label('Approve (Act)'))
      expect(summary({ kind: 'plan' }, plan({ approve: true, permissionMode: 'auto-medium' }), labels)).toEqual(label('Approve (Auto (Medium))'))
      expect(summary(prompt, plan({ approve: true, choiceId: 'fast', permissionMode: 'act' }), labels)).toEqual(label('Approve: the fast path (Act)'))
    })

    // An agent whose settings the browser does not hold, such as one that closed,
    // states no label, so the row names the mode by its value.
    it('reads a mode whose label is unknown by its value', () => {
      expect(summary({ kind: 'plan' }, plan({ approve: true, permissionMode: 'acceptEdits' }))).toEqual(label('Approve (acceptEdits)'))
      expect(summary({ kind: 'plan' }, plan({ approve: true, permissionMode: 'acceptEdits' }), { acceptEdits: '' })).toEqual(label('Approve (acceptEdits)'))
    })

    it('reads nothing for a choice that the prompt does not offer', () => {
      expect(summary(prompt, plan({ approve: true, choiceId: 'slow' }))).toBeNull()
      expect(summary({ kind: 'plan' }, plan({ choiceId: 'fast' }))).toBeNull()
    })
  })

  describe('a question', () => {
    const color: ControlQuestion = {
      id: 'color',
      header: 'Color',
      question: 'Which color do you prefer?',
      options: [
        { value: 'r', label: 'Red' },
        { value: 'b', label: 'Blue' },
      ],
    }
    const sizes: ControlQuestion = {
      id: 'sizes',
      header: 'Sizes',
      question: 'Which sizes?',
      multiSelect: true,
      options: [
        { label: 'Small' },
        { label: 'Large' },
      ],
    }
    const prompt = (...list: ControlQuestion[]): ControlPrompt => ({ kind: 'question', questions: list })

    it('reads each answered question under its text, with the labels of its options', () => {
      expect(summary(prompt(color, sizes), questions([
        {
          selected: ['b'],
        },
        {
          selected: ['Large', 'Small'],
        },
      ]))).toEqual(label('Which color do you prefer?: Blue\nWhich sizes?: Large, Small'))
    })

    // A multiple choice holds its typed text as one more answer, and a prompt that
    // keeps a note beside a selection holds the note after it.
    it('reads typed text after the chosen options', () => {
      expect(summary(prompt(sizes), questions([
        {
          selected: ['Small'],
          customText: ' Medium ',
        },
      ])))
        .toEqual(label('Which sizes?: Small, Medium'))
      expect(summary({
        kind: 'question',
        questions: [color],
        preservesSelectionNotes: true,
      }, questions([
        {
          selected: ['r'],
          customText: 'Keep it dark.',
        },
      ])))
        .toEqual(label('Which color do you prefer?: Red, Keep it dark.'))
      expect(summary(prompt(color), questions([
        { customText: 'Green' },
      ])))
        .toEqual(label('Which color do you prefer?: Green'))
    })

    it('reads a value that no option states as it stands', () => {
      expect(summary(prompt(color), questions([
        {
          selected: ['g'],
        },
      ]))).toEqual(label('Which color do you prefer?: g'))
    })

    it('titles a question by its header, its id, or its place when it states no text', () => {
      const untitled = (fields: Partial<ControlQuestion>): ControlQuestion => ({ question: '', options: [], ...fields })
      expect(summary(prompt(
        untitled({ header: 'Color' }),
        untitled({ id: 'size' }),
        untitled({ question: '  ' }),
      ), questions([
        { customText: 'Red' },
        { customText: 'Large' },
        { customText: 'Soon' },
      ]))).toEqual(label('Color: Red\nsize: Large\nQuestion 3: Soon'))
    })

    it('leaves out a question that the answer leaves blank', () => {
      expect(summary(prompt(color, sizes), questions([
        {},
        {
          selected: ['Small'],
        },
      ]))).toEqual(label('Which sizes?: Small'))
      expect(summary(prompt(color, sizes), questions([
        {
          selected: ['r'],
        },
      ])))
        .toEqual(label('Which color do you prefer?: Red'))
    })

    it('reads an answer that answers no question as No answer', () => {
      expect(summary(prompt(color), questions([
        { customText: '  ' },
      ]))).toEqual(label('No answer'))
      expect(summary(prompt(color), questions([]))).toEqual(label('No answer'))
    })

    it('reads a dismissal with the title of the question it dismissed', () => {
      expect(summary(prompt(color), cancelledQuestions())).toEqual(label('Dismissed: Which color do you prefer?'))
    })

    // A title of several words reads apart from the next one only on a line of its own.
    it('reads a dismissal of several questions with one title on each line', () => {
      expect(summary(prompt(color, sizes), cancelledQuestions())).toEqual(label('Dismissed:\nWhich color do you prefer?\nWhich sizes?'))
    })

    it('reads a dismissal of no question as Dismissed', () => {
      expect(summary(prompt(), cancelledQuestions())).toEqual(label('Dismissed'))
    })

    it('reads a dismissal with its reason as feedback', () => {
      expect(summary(prompt(color), cancelledQuestions('User stopped'))).toEqual(feedback('User stopped'))
    })

    // A question can state its text over several lines, and a title on two lines
    // would read as two titles.
    it('reads a title of several lines on one line', () => {
      const multiline: ControlQuestion = { question: 'docs\n\n  Proceed?', options: [] }
      expect(summary(prompt(multiline), questions([
        { customText: 'Yes' },
      ]))).toEqual(label('docs Proceed?: Yes'))
      expect(summary(prompt(multiline, sizes), cancelledQuestions())).toEqual(label('Dismissed:\ndocs Proceed?\nWhich sizes?'))
    })
  })

  describe('a form', () => {
    const prompt: ControlPrompt = {
      kind: 'elicitation',
      elicitation: {
        mode: 'form',
        message: 'Configure the deployment',
        schema: {
          type: 'object',
          properties: {
            replicas: { type: 'integer', title: 'Replica count' },
            region: {
              type: 'string',
              title: 'Region',
              enum: ['us', 'eu'],
              enumNames: ['United States', 'Europe'],
            },
            notify: { type: 'boolean', title: 'Notify' },
            tags: {
              type: 'array',
              items: { type: 'string' },
            },
          },
        },
      },
    }

    it('reads each field under its title, and a value of an option as the option\'s label', () => {
      expect(summary(prompt, elicitation(ElicitationAction.ACCEPT, JSON.stringify({
        replicas: 0,
        region: 'eu',
        notify: false,
        tags: ['a', 'b'],
      })))).toEqual(label('Approved\nReplica count: 0\nRegion: Europe\nNotify: No\ntags: ["a", "b"]'))
    })

    it('reads a field that the schema does not state by its key', () => {
      expect(summary(prompt, elicitation(ElicitationAction.ACCEPT, '{"extra":"value"}')))
        .toEqual(label('Approved\nextra: value'))
    })

    it('reads an accept with no form, or with content that is no JSON object, as Approved', () => {
      expect(summary(prompt, elicitation(ElicitationAction.ACCEPT))).toEqual(label('Approved'))
      expect(summary(prompt, elicitation(ElicitationAction.ACCEPT, '[1]'))).toEqual(label('Approved'))
      expect(summary(prompt, elicitation(ElicitationAction.ACCEPT, '{unfinished'))).toEqual(label('Approved'))
    })

    it('reads a decline and a cancel', () => {
      expect(summary(prompt, elicitation(ElicitationAction.DECLINE))).toEqual(label('Rejected'))
      expect(summary(prompt, elicitation(ElicitationAction.CANCEL))).toEqual(label('Cancelled'))
    })

    it('reads nothing for a decision that states no action', () => {
      expect(summary(prompt, elicitation(ElicitationAction.UNSPECIFIED))).toBeNull()
    })
  })

  describe('a dialog', () => {
    const confirm: ControlPrompt = {
      kind: 'dialog',
      dialog: { title: 'Proceed?', variant: 'confirm' },
    }
    const editor: ControlPrompt = {
      kind: 'dialog',
      dialog: { title: 'Edit the note', variant: 'editor' },
    }

    it('reads a confirmation, a refusal and a cancellation', () => {
      expect(summary(confirm, dialog(DialogAction.CONFIRM))).toEqual(label('Approved'))
      expect(summary(confirm, dialog(DialogAction.REFUSE))).toEqual(label('Rejected'))
      expect(summary(editor, dialog(DialogAction.CANCEL))).toEqual(label('Cancelled'))
    })

    // A value is the text that the runtime received, so it keeps its white space.
    it('reads the value of a submit exactly, and an empty value as Empty answer', () => {
      expect(summary(editor, dialog(DialogAction.SUBMIT, '  first line\nsecond  '))).toEqual(label('  first line\nsecond  '))
      expect(summary(editor, dialog(DialogAction.SUBMIT))).toEqual(label('Empty answer'))
    })

    it('reads nothing for a decision that states no action', () => {
      expect(summary(confirm, dialog(DialogAction.UNSPECIFIED))).toBeNull()
    })
  })

  it('reads nothing for a decision of another kind than its prompt', () => {
    expect(summary({ kind: 'plan' }, permission('once'))).toBeNull()
    expect(summary(permissionPrompt([...ALLOW_DENY_OPTIONS]), plan({ approve: true }))).toBeNull()
    expect(summary({ kind: 'plan' }, questions([]))).toBeNull()
    expect(summary({ kind: 'plan' }, elicitation(ElicitationAction.CANCEL))).toBeNull()
    expect(summary({ kind: 'plan' }, dialog(DialogAction.CANCEL))).toBeNull()
  })

  it('reads nothing for a decision of no kind', () => {
    expect(summary({ kind: 'plan' }, create(ControlDecisionSchema, {}))).toBeNull()
  })
})
