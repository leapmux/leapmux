import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION, PI_PLAN_DIALOG } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'

/** Keep the selected implementation settings visible when the user approves the plan. */
export function piPlanApprovalDetails(payload: Record<string, unknown>): string[] {
  const lines = pickString(payload, 'title').split('\n').map(line => line.trim())
  return ['Model: ', 'Plan reinjection: '].flatMap((prefix) => {
    const line = lines.find(line => line.startsWith(prefix))
    return line ? [line] : []
  })
}

/** Match the native menu before assigning approval semantics to its string choices. */
export function isPiPlanApproval(payload: Record<string, unknown>): boolean {
  if (payload.type !== PI_EVENT.ExtensionUIRequest || payload.method !== PI_DIALOG_METHOD.Select
    || pickString(payload, 'title').split('\n', 1)[0].trim() !== PI_PLAN_DIALOG.ReadyTitle) {
    return false
  }
  const options = payload.options
  return Array.isArray(options)
    && options.every(option => typeof option === 'string' && option.trim())
    && new Set(options).size === options.length
    && Object.values(PI_PLAN_ACTION).every(option => options.includes(option))
}
