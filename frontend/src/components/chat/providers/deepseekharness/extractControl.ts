import type { ControlExtractionInput, ExtractedControlRequest } from '../capabilities'
import { ACP_PERMISSION_KIND } from '~/generated/contracts/acp-protocol'
import { DEEPSEEK_HARNESS_CONTROL_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { deepseekHarnessQuestionRecords } from './askUserQuestion'

export function deepseekHarnessExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  if (payload.event === DEEPSEEK_HARNESS_CONTROL_EVENT.UserQuestions) {
    const questions = deepseekHarnessQuestionRecords(payload)
    const plan = questions.length === 1 ? questions[0] : undefined
    const intent = pickObject(plan, 'intent')
    if (!plan || intent?.kind !== 'plan-review')
      return null
    const text = pickString(plan, 'detail')
    const approve = pickString(intent, 'approve')
    const options = Array.isArray(plan.options) ? plan.options.filter(isObject) : []
    return {
      kind: 'plan',
      ...(text ? { text } : {}),
      choices: options.flatMap((option) => {
        const label = pickString(option, 'label')
        return label ? [{ id: label, label, approves: label === approve }] : []
      }),
    }
  }
  if (payload.event !== DEEPSEEK_HARNESS_CONTROL_EVENT.Approval)
    return null
  const request = pickObject(payload, 'request')
  const title = pickString(request, 'toolName')
  if (!title)
    return null
  // DeepSeek Harness's own client shows the display text, and `reason` is its English audit record.
  const reason = pickString(pickObject(request, 'displayReason'), 'en') || pickString(request, 'reason')
  return {
    kind: 'permission',
    permission: {
      title,
      ...(reason ? { reason } : {}),
      options: [
        { optionId: 'allow', name: 'Allow once', kind: ACP_PERMISSION_KIND.AllowOnce },
        { optionId: 'deny', name: 'Deny', kind: KIND_REJECT_ONCE },
      ],
    },
  }
}
