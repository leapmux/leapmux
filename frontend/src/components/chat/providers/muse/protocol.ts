import { MUSE_ITEM_KIND, MUSE_METHOD } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

const ITEM_METHODS: ReadonlySet<string> = new Set([MUSE_METHOD.ItemStarted, MUSE_METHOD.ItemUpdated, MUSE_METHOD.ItemCompleted])

/** These wrapper tokens belong to the browser's workflow result reader. */
export const MUSE_WORKFLOW_RECONCILIATION = {
  Open: '<workflow-launch-reconciled>',
  Close: '</workflow-launch-reconciled>',
  Type: 'workflow_launch_reconciled',
} as const

/** Read only Muse's native item envelopes. */
export function museItem(payload: unknown): Record<string, unknown> | undefined {
  if (!isObject(payload) || typeof payload.method !== 'string' || !ITEM_METHODS.has(payload.method))
    return undefined
  const item = pickObject(pickObject(payload, 'params'), 'item')
  return item && pickString(item, 'itemId') && pickString(item, 'kind')
    ? item
    : undefined
}

export function museParams(payload: unknown): Record<string, unknown> | undefined {
  return isObject(payload) && typeof payload.method === 'string' && payload.method !== ''
    ? pickObject(payload, 'params') ?? undefined
    : undefined
}

export function museItemText(item: Record<string, unknown> | undefined): string {
  if (item?.kind === MUSE_ITEM_KIND.Reasoning && Array.isArray(item.summary))
    return item.summary.filter((part): part is string => typeof part === 'string').join('\n\n') || pickString(item, 'text')
  return pickString(item, 'text')
}
