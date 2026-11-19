import type { ControlPrompt, PermissionOption, PlanChoice } from '../model/controlPrompt'
import type { ControlQuestion } from '../model/question'
import type { ControlAnswerState } from './types'
import type { McpElicitationAction } from '~/generated/contracts/mcp-elicitation'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { create } from '@bufbuild/protobuf'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { ControlDecisionSchema, DialogAction, ElicitationAction } from '~/generated/proto/leapmux/v1/agent_control_pb'

/**
 * The decisions that answer a control request.
 *
 * A decision is neutral: it states which option, which answers or which action the
 * reader chose, and the Worker's provider package builds the provider's own answer
 * from it (AnswerControl). These builders are the only place that a decision is
 * spelled, so the banner and the composer cannot answer one request in two shapes.
 */

/** Choose one option of a permission prompt, with the reason of a refusal. */
export function permissionDecision(optionId: string, feedback = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'permission',
      value: { optionId, feedback },
    },
  })
}

/** The settings that an approval of a plan applies. */
export interface PlanApproval {
  permissionMode: string
  clearContext: boolean
}

/** Approve a plan, with one of its approving choices or none. */
export function approvePlanDecision(approval: PlanApproval, choiceId = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'plan',
      value: {
        approve: true,
        choiceId,
        permissionMode: approval.permissionMode,
        clearContext: approval.clearContext,
      },
    },
  })
}

/** Refuse a plan, with what to change. */
export function refusePlanDecision(feedback = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'plan',
      value: { approve: false, feedback },
    },
  })
}

/**
 * Choose one of a plan's own choices. An approving choice carries the approval
 * settings, because it approves the same plan; any other choice carries none.
 */
export function planChoiceDecision(choice: PlanChoice, approval: PlanApproval): ControlDecision {
  return choice.effect === 'approve'
    ? approvePlanDecision(approval, choice.id)
    : create(ControlDecisionSchema, {
        kind: {
          case: 'plan',
          value: { approve: false, choiceId: choice.id },
        },
      })
}

/**
 * Whether typed text and chosen options coexist in ONE answer to question:
 *
 *   - A multiple-choice question's answer holds both: choosing A and typing X answers
 *     A and X.
 *   - A prompt that keeps a note beside a selection (`preservesSelectionNotes`) holds
 *     both on any question: the text is a note on the chosen option.
 *
 * Otherwise one replaces the other: choosing an option clears the typed text, and
 * typing clears the chosen option.
 */
export function typedWithOptions(question: ControlQuestion | undefined, preservesSelectionNotes: boolean): boolean {
  return question?.multiSelect === true || preservesSelectionNotes
}

/** The questions of a question prompt, and whether it keeps a note beside a selection. */
export type QuestionSet = Pick<Extract<ControlPrompt, { kind: 'question' }>, 'questions' | 'preservesSelectionNotes'>

/**
 * Answer the questions from the reader's answer state: the selected option values of
 * each question, and the text typed for it.
 *
 * Where the typed text cannot stand beside a selection ({@link typedWithOptions}), a
 * selection drops the text, as choosing the option on screen does. A restored answer
 * can hold both, and the Worker refuses an answer that is two things.
 */
export function questionDecision(state: ControlAnswerState, prompt: QuestionSet): ControlDecision {
  const selections = state.selections()
  const customTexts = state.customTexts()
  const keepsNotes = prompt.preservesSelectionNotes ?? false
  return create(ControlDecisionSchema, {
    kind: {
      case: 'question',
      value: {
        answers: prompt.questions.map((question, index) => {
          const selected = [
            ...(selections[index] ?? []),
          ]
          return {
            selected,
            customText: selected.length > 0 && !typedWithOptions(question, keepsNotes)
              ? ''
              : customTexts[index] ?? '',
          }
        }),
      },
    },
  })
}

/** Refuse to answer the questions, with the reason. */
export function cancelQuestionDecision(feedback = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'question',
      value: { cancelled: true, feedback },
    },
  })
}

const ELICITATION_ACTIONS: Record<McpElicitationAction, ElicitationAction> = {
  [MCP_ELICITATION_ACTION.Accept]: ElicitationAction.ACCEPT,
  [MCP_ELICITATION_ACTION.Decline]: ElicitationAction.DECLINE,
  [MCP_ELICITATION_ACTION.Cancel]: ElicitationAction.CANCEL,
}

/** Answer an MCP elicitation: its action, and for an accept the form and the accept choice. */
export function elicitationDecision(action: McpElicitationAction, content?: Record<string, unknown>, acceptChoiceKey = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'elicitation',
      value: {
        action: ELICITATION_ACTIONS[action],
        contentJson: action === MCP_ELICITATION_ACTION.Accept && content
          ? JSON.stringify(content)
          : '',
        acceptChoiceKey: action === MCP_ELICITATION_ACTION.Accept
          ? acceptChoiceKey
          : '',
      },
    },
  })
}

/** The answers of an extension dialog. */
export type DialogChoice = 'confirm' | 'refuse' | 'submit' | 'cancel'

const DIALOG_ACTIONS: Record<DialogChoice, DialogAction> = {
  confirm: DialogAction.CONFIRM,
  refuse: DialogAction.REFUSE,
  submit: DialogAction.SUBMIT,
  cancel: DialogAction.CANCEL,
}

/** Answer an extension dialog. `value` is the text of a submit. */
export function dialogDecision(action: DialogChoice, value = ''): ControlDecision {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'dialog',
      value: { action: DIALOG_ACTIONS[action], value },
    },
  })
}

/** The first option of one of kinds, in the order of kinds, or undefined. */
function firstOptionOf(options: readonly PermissionOption[], ...kinds: string[]): PermissionOption | undefined {
  for (const kind of kinds) {
    const option = options.find(candidate => candidate.kind === kind)
    if (option)
      return option
  }
  return undefined
}

/**
 * The decision that the composer sends for typed text, by the kind of the prompt, or
 * undefined for a prompt that takes no composer text:
 *
 *   - A permission: an empty send takes the one-time approval, so Enter approves a call
 *     once; typed text refuses with the one-time refusal, or else the remembered one,
 *     and the text is the reason.
 *   - A plan: any send refuses it, and the text says what to change. Approve has its
 *     own button.
 *
 * A question answers through its own form (`questionDecision`), and a form or a
 * dialog draws its own fields.
 */
export function composerDecision(prompt: ControlPrompt, text: string): ControlDecision | undefined {
  switch (prompt.kind) {
    case 'permission': {
      const options = prompt.permission.options
      const option = text.trim() === ''
        ? firstOptionOf(options, ACP_PERMISSION_KIND.AllowOnce)
        : firstOptionOf(options, ACP_PERMISSION_KIND.RejectOnce, ACP_PERMISSION_KIND.RejectAlways)
      if (!option)
        return undefined
      return permissionDecision(option.optionId, text.trim() === ''
        ? ''
        : text)
    }
    case 'plan':
      return refusePlanDecision(text)
    case 'question':
    case 'elicitation':
    case 'dialog':
      return undefined
  }
}
