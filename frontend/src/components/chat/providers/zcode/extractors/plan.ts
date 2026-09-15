import { ZCODE_INTERACTION, ZCODE_METHOD } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Read a plan whose only persisted source is its native approval request. */
export function zcodeControlPlanText(parsed: unknown): string | null {
  if (!isObject(parsed) || parsed.method !== ZCODE_METHOD.RequestUserInput)
    return null
  const params = pickObject(parsed, 'params')
  if (pickObject(params, 'schema')?.interaction !== ZCODE_INTERACTION.PlanApproval)
    return null
  for (const content of [pickObject(params, 'input'), pickObject(params, 'context')]) {
    const plan = pickString(content, 'plan')
    if (plan.trim())
      return plan
  }
  return pickString(params, 'prompt')
}
