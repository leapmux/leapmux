import type { DraftMetadataRecord } from '~/lib/editor/draftPersistence'
import { batch, createSignal } from 'solid-js'
import { createStore, reconcile } from 'solid-js/store'
import { localStorageDrop, localStorageDropPrefix, localStorageLoad, localStorageStore, onStorageAccountChange, PREFIX_COMPOSER_SCHEDULE_DRAFT } from '~/lib/browserStorage'
import { createLogger } from '~/lib/logger'
import { queueEditDraftKeyPrefix } from './draftIdentity'

const log = createLogger('scheduleDrafts')

/**
 * Schedule drafts share the text and attachment draft identity. The agent ID
 * identifies the ordinary composer, and queueEditDraftKey identifies an edit.
 *
 * The cache survives composer unmounts. Durable entries restore it after a
 * reload, before the composer permits a send. An absent entry uses the edited
 * item's stored schedule, or no schedule for an ordinary draft. An explicit
 * empty entry records that the user cleared the schedule.
 */
const [scheduleDrafts, setScheduleDrafts] = createStore<Record<string, string | undefined>>({})
const [accountEpoch, setAccountEpoch] = createSignal(0)
const versions = new Map<string, object>()
const loading = new Map<string, Promise<void>>()

function resetAccount() {
  versions.clear()
  loading.clear()
  batch(() => {
    setScheduleDrafts(reconcile({}))
    setAccountEpoch(value => value + 1)
  })
}

/** The cache generation changes synchronously with its storage account. */
export function scheduleDraftEpoch(): number {
  return accountEpoch()
}

/** Read a cached schedule, or undefined when no override exists. */
export function getScheduleDraft(key: string): string | undefined {
  return scheduleDrafts[key]
}

/**
 * Load one draft before enabling its composer. A later edit, clear, or account
 * change invalidates the read, so old data cannot replace a newer draft.
 */
export function hydrateScheduleDraft(key: string): Promise<void> {
  onStorageAccountChange(resetAccount)
  const pending = loading.get(key)
  if (pending)
    return pending
  const epoch = accountEpoch()
  const version = {}
  versions.set(key, version)
  const request = localStorageLoad<unknown>(`${PREFIX_COMPOSER_SCHEDULE_DRAFT}${key}`).then((value) => {
    if (accountEpoch() === epoch && versions.get(key) === version) {
      setScheduleDrafts(key, typeof value === 'string'
        ? value
        : undefined)
    }
  }).finally(() => {
    if (loading.get(key) === request)
      loading.delete(key)
  })
  loading.set(key, request)
  return request
}

/** Wait for an already hydrated cache, or load it before an editor can save. */
export function prepareScheduleDraft(key: string): Promise<void> {
  const pending = loading.get(key)
  if (pending)
    return pending
  return versions.has(key)
    ? Promise.resolve()
    : hydrateScheduleDraft(key)
}

/** The metadata saved with the text of this exact document key. */
export function scheduleDraftMetadataRecords(key: string): readonly DraftMetadataRecord[] {
  const value = getScheduleDraft(key)
  return value === undefined
    ? []
    : [
        { name: `${PREFIX_COMPOSER_SCHEDULE_DRAFT}${key}`, value },
      ]
}

/** Record and persist the schedule draft of one composer key. */
export function setScheduleDraft(key: string, value: string): void {
  onStorageAccountChange(resetAccount)
  versions.set(key, {})
  setScheduleDrafts(key, value)
  localStorageStore(`${PREFIX_COMPOSER_SCHEDULE_DRAFT}${key}`, value)
}

/** Remove the cached and durable override of one composer key. */
export function clearScheduleDraft(key: string): void {
  versions.delete(key)
  setScheduleDrafts(key, undefined)
  localStorageDrop(`${PREFIX_COMPOSER_SCHEDULE_DRAFT}${key}`)
}

/** Remove every draft of a retired agent, including edits not hydrated yet. */
export function forgetAgentScheduleDrafts(agentId: string): Promise<void> {
  const queuePrefix = queueEditDraftKeyPrefix(agentId)
  const keys = new Set([
    ...Object.keys(scheduleDrafts),
    ...versions.keys(),
    ...loading.keys(),
  ])
  keys.add(agentId)
  batch(() => {
    for (const key of keys) {
      if (key === agentId || key.startsWith(queuePrefix))
        clearScheduleDraft(key)
    }
  })
  return localStorageDropPrefix(`${PREFIX_COMPOSER_SCHEDULE_DRAFT}${queuePrefix}`).catch((error) => {
    log.warn('Failed to remove retired schedule drafts:', error)
  })
}
