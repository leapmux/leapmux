import type { ControlPayloadFault, ControlPrompt, DialogPrompt, ElicitationFieldRule, ElicitationRequest, PermissionOption, PermissionPrompt, PermissionScope, PlanChoice, PlanPermission } from './controlPrompt'
import type { ControlQuestion, QuestionOption } from './question'
import type {
  ControlPrompt as WireControlPrompt,
  ControlQuestion as WireControlQuestion,
  DialogPrompt as WireDialogPrompt,
  ElicitationFieldCondition as WireElicitationFieldCondition,
  ElicitationFieldRule as WireElicitationFieldRule,
  ElicitationPrompt as WireElicitationPrompt,
  PermissionOption as WirePermissionOption,
  PermissionPrompt as WirePermissionPrompt,
  PlanPrompt as WirePlanPrompt,
} from '~/generated/proto/leapmux/v1/agent_control_pb'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import {
  ControlEditorPurpose,
  ControlPayloadFault as ControlPayloadFaultWire,
  DialogVariant,
  ElicitationFieldComparison,
  ElicitationPurpose,
  PermissionOptionKind,
  PlanChoiceEffect,
  PermissionScope as WirePermissionScope,
} from '~/generated/proto/leapmux/v1/agent_control_pb'

/**
 * What the composer's editor does while one request waits: its text explains a
 * refusal (`feedback`), answers the request (`answer`), or is no part of it (`none`).
 */
export type ControlEditorRole = 'feedback' | 'answer' | 'none'

/**
 * The prompt that the Worker read from one control request, in the model that the
 * control banner draws.
 *
 * The Worker's provider package reads the provider's own payload, so this reads no
 * provider format at all: it only moves the neutral wire shape into the model. A
 * request with no prompt at all, which a Worker never sends, reads as a permission
 * with no options.
 */
export function controlPromptFromWire(prompt: WireControlPrompt | undefined): ControlPrompt {
  const kind = prompt?.kind
  switch (kind?.case) {
    case 'permission':
      return {
        kind: 'permission',
        permission: permissionFromWire(kind.value),
      }
    case 'plan':
      return {
        kind: 'plan',
        ...planFromWire(kind.value),
      }
    case 'question':
      return {
        kind: 'question',
        questions: kind.value.questions.map(questionFromWire),
        ...(kind.value.preservesSelectionNotes
          ? { preservesSelectionNotes: true }
          : {}),
      }
    case 'elicitation':
      return {
        kind: 'elicitation',
        elicitation: elicitationFromWire(kind.value),
      }
    case 'dialog':
      return {
        kind: 'dialog',
        dialog: dialogFromWire(kind.value),
      }
    default:
      return {
        kind: 'permission',
        permission: { options: [] },
      }
  }
}

/**
 * Why the Worker could not read a request's payload, or undefined for a payload that it
 * read. Such a request offers no answer: the banner states the fault, and the stop is
 * the way out.
 */
export function controlPayloadFaultFromWire(prompt: WireControlPrompt | undefined): ControlPayloadFault | undefined {
  if (prompt?.kind.case !== 'unreadable')
    return undefined
  return prompt.kind.value === ControlPayloadFaultWire.NOT_AN_OBJECT
    ? 'not-an-object'
    : 'malformed'
}

/** The editor role of one prompt. An unstated purpose is feedback. */
export function controlEditorRoleFromWire(prompt: WireControlPrompt | undefined): ControlEditorRole {
  switch (prompt?.editorPurpose) {
    case ControlEditorPurpose.ANSWER:
      return 'answer'
    case ControlEditorPurpose.NONE:
      return 'none'
    default:
      return 'feedback'
  }
}

/**
 * Parse JSON text that the Worker read from a payload, or undefined for empty text.
 * Text that does not parse stays a string, so a reader still sees what arrived.
 */
function parseWireJson(text: string): unknown {
  if (text === '')
    return undefined
  try {
    return JSON.parse(text) as unknown
  }
  catch {
    return text
  }
}

/** Set `key` on `target` only for a non-empty string, as the model's optional fields expect. */
function assignText<T extends object, K extends keyof T>(target: T, key: K, value: string): void {
  if (value !== '')
    target[key] = value as T[K]
}

function permissionFromWire(wire: WirePermissionPrompt): PermissionPrompt {
  const permission: PermissionPrompt = {
    options: wire.options.map(permissionOptionFromWire),
  }
  assignText(permission, 'title', wire.title)
  assignText(permission, 'reason', wire.reason)
  assignText(permission, 'text', wire.text)
  assignText(permission, 'command', wire.command)
  assignText(permission, 'workingDirectory', wire.workingDirectory)
  const input = parseWireJson(wire.inputJson)
  if (input !== undefined)
    permission.input = input
  if (wire.independentOptions)
    permission.independentOptions = true
  return permission
}

/** The option kinds of the model. An option of another kind keeps its name as its label. */
const OPTION_KINDS: Record<PermissionOptionKind, string> = {
  [PermissionOptionKind.UNSPECIFIED]: 'other',
  [PermissionOptionKind.ALLOW_ONCE]: ACP_PERMISSION_KIND.AllowOnce,
  [PermissionOptionKind.ALLOW_ALWAYS]: ACP_PERMISSION_KIND.AllowAlways,
  [PermissionOptionKind.REJECT_ONCE]: ACP_PERMISSION_KIND.RejectOnce,
  [PermissionOptionKind.REJECT_ALWAYS]: ACP_PERMISSION_KIND.RejectAlways,
  [PermissionOptionKind.OTHER]: 'other',
}

const SCOPES: Partial<Record<WirePermissionScope, PermissionScope>> = {
  [WirePermissionScope.SESSION]: 'session',
  [WirePermissionScope.WORKSPACE]: 'workspace',
  [WirePermissionScope.PROJECT]: 'project',
  [WirePermissionScope.USER]: 'user',
}

function permissionOptionFromWire(wire: WirePermissionOption): PermissionOption {
  const option: PermissionOption = { optionId: wire.optionId, kind: OPTION_KINDS[wire.kind] ?? 'other' }
  assignText(option, 'name', wire.name)
  const scope = SCOPES[wire.scope]
  if (scope)
    option.scope = scope
  if (wire.endsTurn)
    option.endsTurn = true
  return option
}

/** The effects of the model. A choice of no stated effect is `other`: nothing says what it does. */
const PLAN_CHOICE_EFFECTS: Partial<Record<PlanChoiceEffect, PlanChoice['effect']>> = {
  [PlanChoiceEffect.APPROVE]: 'approve',
  [PlanChoiceEffect.REFUSE]: 'refuse',
  [PlanChoiceEffect.OTHER]: 'other',
}

function planFromWire(wire: WirePlanPrompt): Omit<Extract<ControlPrompt, { kind: 'plan' }>, 'kind'> {
  const plan: { text?: string, permissions?: PlanPermission[], details?: string[], choices?: PlanChoice[] } = {}
  assignText(plan, 'text', wire.text)
  if (wire.permissions.length > 0)
    plan.permissions = wire.permissions.map(permission => ({ tool: permission.tool, prompt: permission.prompt }))
  if (wire.details.length > 0)
    plan.details = [...wire.details]
  if (wire.choices.length > 0) {
    plan.choices = wire.choices.map((choice) => {
      const planChoice: PlanChoice = { id: choice.id, label: choice.label, effect: PLAN_CHOICE_EFFECTS[choice.effect] ?? 'other' }
      assignText(planChoice, 'description', choice.description)
      return planChoice
    })
  }
  return plan
}

function questionFromWire(wire: WireControlQuestion): ControlQuestion {
  const question: ControlQuestion = {
    question: wire.question,
    options: wire.options.map((option) => {
      const read: QuestionOption = { label: option.label }
      if (option.value !== undefined)
        read.value = option.value
      assignText(read, 'description', option.description)
      assignText(read, 'preview', option.preview)
      return read
    }),
  }
  assignText(question, 'id', wire.id)
  assignText(question, 'header', wire.header)
  assignText(question, 'context', wire.context)
  if (wire.multiSelect)
    question.multiSelect = true
  if (wire.allowEmpty)
    question.allowEmpty = true
  if (wire.allowCustomText !== undefined)
    question.allowCustomText = wire.allowCustomText
  if (wire.minimumSelections !== undefined)
    question.minimumSelections = wire.minimumSelections
  if (wire.maximumSelections !== undefined)
    question.maximumSelections = wire.maximumSelections
  // A set selection, even an empty one, is the runtime's own answer: the
  // composer starts from it.
  if (wire.initialSelections !== undefined)
    question.initialSelections = [...wire.initialSelections.values]
  return question
}

function elicitationFromWire(wire: WireElicitationPrompt): ElicitationRequest {
  const elicitation: ElicitationRequest = { mode: wire.mode, message: wire.message }
  if (wire.purpose === ElicitationPurpose.PERMISSION)
    elicitation.purpose = 'permission'
  assignText(elicitation, 'title', wire.title)
  assignText(elicitation, 'description', wire.description)
  assignText(elicitation, 'server', wire.server)
  assignText(elicitation, 'url', wire.url)
  assignText(elicitation, 'argumentNotice', wire.argumentNotice)
  const schema = parseWireJson(wire.schemaJson)
  if (schema !== undefined)
    elicitation.schema = schema
  const args = parseWireJson(wire.argumentsJson)
  if (args !== undefined)
    elicitation.arguments = args
  if (wire.acceptChoices.length > 0)
    elicitation.acceptChoices = wire.acceptChoices.map(choice => ({ key: choice.key, label: choice.label }))
  if (wire.fieldRules.length > 0)
    elicitation.fieldRules = wire.fieldRules.map(fieldRuleFromWire)
  return elicitation
}

function fieldRuleFromWire(wire: WireElicitationFieldRule): ElicitationFieldRule {
  const rule: ElicitationFieldRule = { key: wire.key }
  if (wire.hidden)
    rule.hidden = true
  const conditions = wire.conditions.flatMap(fieldConditionFromWire)
  if (conditions.length > 0)
    rule.conditions = conditions
  return rule
}

type FieldCondition = NonNullable<ElicitationFieldRule['conditions']>[number]

const FIELD_COMPARISONS: Partial<Record<ElicitationFieldComparison, FieldCondition['comparison']>> = {
  [ElicitationFieldComparison.EQUAL]: 'equal',
  [ElicitationFieldComparison.NOT_EQUAL]: 'not-equal',
}

/**
 * One condition of a field rule, or none. A condition of an unknown comparison, or
 * whose value is not the JSON text of a string, a number or a boolean, states
 * nothing, so the form drops it, as a reader of the runtime's form drops it.
 */
function fieldConditionFromWire(wire: WireElicitationFieldCondition): FieldCondition[] {
  const comparison = FIELD_COMPARISONS[wire.comparison]
  let value: unknown
  try {
    value = JSON.parse(wire.valueJson) as unknown
  }
  catch {
    return []
  }
  if (!comparison || (typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean'))
    return []
  return [
    { field: wire.field, comparison, value },
  ]
}

const DIALOG_VARIANTS: Partial<Record<DialogVariant, DialogPrompt['variant']>> = {
  [DialogVariant.CONFIRM]: 'confirm',
  [DialogVariant.INPUT]: 'input',
  [DialogVariant.EDITOR]: 'editor',
}

function dialogFromWire(wire: WireDialogPrompt): DialogPrompt {
  const dialog: DialogPrompt = { title: wire.title, variant: DIALOG_VARIANTS[wire.variant] ?? 'confirm' }
  assignText(dialog, 'message', wire.message)
  assignText(dialog, 'placeholder', wire.placeholder)
  assignText(dialog, 'prefill', wire.prefill)
  if (wire.timeoutMs !== undefined)
    dialog.timeoutMs = Number(wire.timeoutMs)
  return dialog
}
