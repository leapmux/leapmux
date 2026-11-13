import type { TextsDiffInput } from '~/components/chat/diff/diffTypes'
import { createMemo, createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { computeDiff } from '~/components/chat/diff/diffSearch'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { heldDiffWorkerClient, holdComputedDiffs, requestedDiffInputs, resetHeldComputedDiffs } from '~/test-support/heldComputedDiffs'

// The mocked client, as a test that mocks the diff worker client receives it.
// Here it wraps the real client, which the stand-in Worker answers. The real
// client keeps every diff that arrives for the whole file, so each test diffs
// texts of its own.
const client = await heldDiffWorkerClient()

afterEach(() => {
  resetHeldComputedDiffs()
})

function texts(oldText: string, newText: string): TextsDiffInput {
  return { kind: 'texts', oldText, newText }
}

/** Ask the client for the diff of `input`, and wait until the stand-in Worker answers it. */
async function arrive(input: TextsDiffInput): Promise<void> {
  client.requestComputedDiff(input)
  await settleWorkerReplies()
  expect(client.requestComputedDiff(input)).toEqual(computeDiff(input))
}

describe('heldDiffWorkerClient', () => {
  it('answers undefined for a diff while the test holds the diffs, and the diff of the real client after the release', async () => {
    const input = texts('held old\n', 'held new\n')
    holdComputedDiffs(true)

    expect(client.requestComputedDiff(input)).toBeUndefined()
    await settleWorkerReplies()
    // The real client never received the request, so nothing arrived.
    expect(client.requestComputedDiff(input)).toBeUndefined()

    holdComputedDiffs(false)
    expect(client.requestComputedDiff(input)).toBeUndefined()
    await settleWorkerReplies()

    expect(client.requestComputedDiff(input)).toEqual(computeDiff(input))
    expect(requestedDiffInputs()).toEqual([input, input, input, input])
  })

  it('answers a diff that needs no search while the test holds the diffs, as the real client does', () => {
    const created = texts('', 'created line\n')
    holdComputedDiffs(true)

    expect(client.requestComputedDiff(created)).toEqual(computeDiff(created))
  })

  it('keeps a diff that arrived ready while the test holds the diffs again', async () => {
    const input = texts('arrived old\n', 'arrived new\n')
    await arrive(input)

    holdComputedDiffs(true)

    expect(client.requestComputedDiff(input)).toEqual(computeDiff(input))
    // An input with equal texts in another object finds the same diff.
    expect(client.requestComputedDiff(texts('arrived old\n', 'arrived new\n'))).toEqual(computeDiff(input))
  })

  it('holds a diff that the real client asked for but had not received when the test held the diffs again', async () => {
    const input = texts('computing old\n', 'computing new\n')
    // Released: the real client asks the stand-in Worker.
    expect(client.requestComputedDiff(input)).toBeUndefined()

    holdComputedDiffs(true)
    await settleWorkerReplies()

    // The real client holds the diff now, but the test held it before it arrived.
    expect(client.requestComputedDiff(input)).toBeUndefined()
    holdComputedDiffs(false)
    expect(client.requestComputedDiff(input)).toEqual(computeDiff(input))
  })

  it('re-runs a memo that waits for a held diff at the release, and no memo whose diff is ready', async () => {
    const ready = texts('ready old\n', 'ready new\n')
    const waiting = texts('waiting old\n', 'waiting new\n')
    await arrive(ready)

    await createRoot(async (dispose) => {
      holdComputedDiffs(true)
      let readyRuns = 0
      let waitingRuns = 0
      const readyDiff = createMemo(() => {
        readyRuns++
        return client.requestComputedDiff(ready)
      })
      const waitingDiff = createMemo(() => {
        waitingRuns++
        return client.requestComputedDiff(waiting)
      })
      expect(readyDiff()).toEqual(computeDiff(ready))
      expect(waitingDiff()).toBeUndefined()

      holdComputedDiffs(false)
      await settleWorkerReplies()

      expect(waitingDiff()).toEqual(computeDiff(waiting))
      expect(waitingRuns).toBeGreaterThan(1)
      expect(readyRuns).toBe(1)
      dispose()
    })
  })

  it('forgets each request and each diff that arrived when the test resets it', async () => {
    const input = texts('reset old\n', 'reset new\n')
    await arrive(input)

    resetHeldComputedDiffs()

    expect(requestedDiffInputs()).toEqual([])
    holdComputedDiffs(true)
    // The real client still holds the diff, but this test has not received it.
    expect(client.requestComputedDiff(input)).toBeUndefined()
  })
})
