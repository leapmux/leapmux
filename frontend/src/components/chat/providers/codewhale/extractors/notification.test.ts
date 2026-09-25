import { describe, expect, it } from 'vitest'
import { CODEWHALE_EVENT, CODEWHALE_ITEM_KIND } from '~/generated/contracts/codewhale-protocol'
import { codewhaleEvent, itemFinished } from '../toolResults.fixtures'
import { codewhaleNotificationEntry } from './notification'

describe('codewhaleNotificationEntry', () => {
  it('states a status item', () => {
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.Status, 'Checkpoint saved'))).toStrictEqual([{ kind: 'status', text: 'Checkpoint saved' }])
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.Status, '  '))).toStrictEqual([])
  })

  // `detail` is the whole text and `summary` repeats its head, so the summary
  // answers only for an item that sends no detail.
  it('states the summary of an item that sends no detail', () => {
    const item = (kind: string, event: string = CODEWHALE_EVENT.ItemCompleted) => codewhaleEvent(event, { item: { kind, summary: ' Saved the checkpoint ', detail: '' } })
    expect(codewhaleNotificationEntry(item(CODEWHALE_ITEM_KIND.Status))).toStrictEqual([{ kind: 'status', text: 'Saved the checkpoint' }])
    expect(codewhaleNotificationEntry(item(CODEWHALE_ITEM_KIND.Error, CODEWHALE_EVENT.ItemFailed))).toStrictEqual([{ kind: 'text', text: 'Error: Saved the checkpoint' }])
  })

  it('states nothing for an item that has not ended', () => {
    const started = (kind: string) => codewhaleEvent(CODEWHALE_EVENT.ItemStarted, { item: { kind, summary: 'x', detail: 'x' } })
    expect(codewhaleNotificationEntry(started(CODEWHALE_ITEM_KIND.Status))).toStrictEqual([])
    expect(codewhaleNotificationEntry(started(CODEWHALE_ITEM_KIND.ContextCompaction))).toStrictEqual([])
    expect(codewhaleNotificationEntry(started(CODEWHALE_ITEM_KIND.Error))).toStrictEqual([])
  })

  it('states a compaction and who started it', () => {
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'Compaction complete', { auto: true })))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto' } }])
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'Compaction complete', { auto: false })))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual' } }])
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'The model refused', {}, CODEWHALE_EVENT.ItemFailed)))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', error: 'The model refused' }])
  })

  // A compaction that did not complete is an error to the reader, even when the
  // runtime gives no words for it.
  it('states a compaction that the turn cut short, with or without its words', () => {
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, '', {}, CODEWHALE_EVENT.ItemInterrupted)))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', error: 'aborted' }])
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'Stopped by the reader', {}, CODEWHALE_EVENT.ItemCanceled)))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', error: 'Stopped by the reader' }])
  })

  // The `auto` flag is a boolean on the wire. Any other value is no statement that
  // the runtime started the compaction by itself.
  it('reads a compaction as manual unless the event says auto is true', () => {
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'done')))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual' } }])
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.ContextCompaction, 'done', { auto: 'true' })))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual' } }])
  })

  it('states an error item', () => {
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.Error, 'Rate limited', {}, CODEWHALE_EVENT.ItemFailed))).toStrictEqual([{ kind: 'text', text: 'Error: Rate limited' }])
  })

  it('states an error item that gives no words', () => {
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.Error, ' \n', {}, CODEWHALE_EVENT.ItemFailed))).toStrictEqual([{ kind: 'text', text: 'Error' }])
  })

  // The worker persists a dropped steer only when nothing sends it again: the queue
  // sends a refused steer as a new message, and the worker hands a later drop back to
  // the queue. So the row asks the reader to send it, and the runtime's own reason,
  // which asks the same of every drop, is not shown.
  it('states a steer that the model never read, and asks the reader to send it again', () => {
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.TurnSteerDropped, { input: '  Also this.\n', reason: 'the turn moved on before the engine committed it, so the model never saw it — resend it' })))
      .toStrictEqual([{ kind: 'text', text: 'Codewhale dropped the steer "Also this." before the model read it. Send it again to deliver it.' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.TurnSteerDropped, {})))
      .toStrictEqual([{ kind: 'text', text: 'Codewhale dropped a steer before the model read it. Send it again to deliver it.' }])
  })

  // Codewhale's configuration sets the wait, and nothing on the wire states it, so the
  // row names the setting that removes it.
  it('states an approval that nobody answered in time, and the setting that sets the wait', () => {
    const setting = 'Set [tools] user_input_timeout_seconds = 0 in the Codewhale configuration to wait with no limit.'
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.ApprovalTimeout, { approval_id: 'a1', timeout_secs: 300 })))
      .toStrictEqual([{ kind: 'text', text: `Codewhale denied the call because nobody answered within 5m. ${setting}` }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.ApprovalTimeout, { timeout_secs: 0 })))
      .toStrictEqual([{ kind: 'text', text: `Codewhale denied the call because nobody answered in time. ${setting}` }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.ApprovalTimeout, {})))
      .toStrictEqual([{ kind: 'text', text: `Codewhale denied the call because nobody answered in time. ${setting}` }])
  })

  it('states a wait that is not a positive number of seconds as no wait at all', () => {
    const setting = 'Set [tools] user_input_timeout_seconds = 0 in the Codewhale configuration to wait with no limit.'
    for (const timeout of [-30, '300', null]) {
      expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.ApprovalTimeout, { timeout_secs: timeout })), String(timeout))
        .toStrictEqual([{ kind: 'text', text: `Codewhale denied the call because nobody answered in time. ${setting}` }])
    }
  })

  it('states a very long and a sub-second wait in the unit that fits', () => {
    const text = (timeout: number) => (codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.ApprovalTimeout, { timeout_secs: timeout }))[0] as { text: string }).text
    expect(text(90_000)).toContain('within 1d 1h')
    expect(text(0.5)).toContain('within 500ms')
  })

  it('states each other runtime notice', () => {
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.SandboxDenied, { tool_name: 'bash', reason: 'network' })))
      .toStrictEqual([{ kind: 'text', text: 'The sandbox denied bash: network' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.SandboxDenied, {})))
      .toStrictEqual([{ kind: 'text', text: 'The sandbox denied a tool' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.StoreFailure, { message: 'disk full' })))
      .toStrictEqual([{ kind: 'text', text: 'The runtime could not save its state: disk full' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.StoreFailure, {})))
      .toStrictEqual([{ kind: 'text', text: 'The runtime could not save its state' }])
  })

  it('reads a store failure\'s reason from each field that can state it, in order', () => {
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.StoreFailure, { error: 'locked', reason: 'second' })))
      .toStrictEqual([{ kind: 'text', text: 'The runtime could not save its state: locked' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.StoreFailure, { reason: 'read-only volume' })))
      .toStrictEqual([{ kind: 'text', text: 'The runtime could not save its state: read-only volume' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.StoreFailure, { message: 'first', error: 'second' })))
      .toStrictEqual([{ kind: 'text', text: 'The runtime could not save its state: first' }])
  })

  it('states the tool alone for a sandbox refusal with no reason, and a blank steer as a steer', () => {
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.SandboxDenied, { tool_name: 'bash', reason: '' })))
      .toStrictEqual([{ kind: 'text', text: 'The sandbox denied bash' }])
    expect(codewhaleNotificationEntry(codewhaleEvent(CODEWHALE_EVENT.TurnSteerDropped, { input: ' \n ' })))
      .toStrictEqual([{ kind: 'text', text: 'Codewhale dropped a steer before the model read it. Send it again to deliver it.' }])
  })

  it('states nothing for a row it does not own', () => {
    expect(codewhaleNotificationEntry({ type: 'interrupted' })).toStrictEqual([])
    expect(codewhaleNotificationEntry(itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello'))).toStrictEqual([])
    expect(codewhaleNotificationEntry(codewhaleEvent('a.later.event', {}))).toStrictEqual([])
  })
})
