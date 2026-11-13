import type { FileEditDiff } from '~/components/chat/model/fileEditDiff'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  answerHeldDiffRequestsUntilIdle,
  installHeldDiffWorker,
  onlyHeldDiffWorker,
  settleDiffReplies,
} from '~/test-support/heldDiffWorker'

// ---------------------------------------------------------------------------
// The Copy and Quote text of an edit row on the browser path
//
// A Worker exists here, so the real diff client computes no diff on the calling
// thread, and nothing but the toolbar meta asks for the diff: these rows draw no
// diff body. The text must still become the final diff once the worker replies,
// and a memo that read the stand-in must re-run, as MessageBubble's Quote text
// memo does.
// ---------------------------------------------------------------------------

afterEach(() => {
  vi.unstubAllGlobals()
})

/**
 * `toolCallMeta`, its fixtures and Solid, imported afresh: the client then spawns
 * the stand-in Worker and starts with no cache, and the memos come from the copy
 * of Solid that the client's signal belongs to.
 */
async function load() {
  vi.resetModules()
  return {
    ...await import('./meta'),
    ...await import('~/test-support/toolCallFixture'),
    ...await import('solid-js'),
  }
}

function change(filePath: string): FileEditDiff {
  return {
    filePath,
    structuredPatch: null,
    oldStr: 'a\nb\nc\n',
    newStr: 'a\nB\nc\n',
  }
}

const STAND_IN = '-a\n-b\n-c\n+a\n+B\n+c\n'
const FINAL = ' a\n-b\n+B\n c\n'

describe('toolCallMeta', () => {
  it('asks for the diff of a paired request row\'s Copy text, and re-runs a memo over the text when it lands', async () => {
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, toolCallFixture, toolCallMeta, toolRow } = await load()
    // The request row of a call that has a result row draws no body: the result
    // row draws the landed change.
    const requestRow = toolRow(toolCallFixture('edit', {
      status: 'completed',
      request: {
        changes: [
          change('/p/a.ts'),
        ],
      },
      result: {
        changes: [
          {
            filePath: '/p/a.ts',
            structuredPatch: null,
            oldStr: 'x',
            newStr: 'y',
          },
        ],
      },
    }), 'request', { result: true })

    await createRoot(async (dispose) => {
      const meta = createMemo(() => toolCallMeta(requestRow))
      const quoted = createMemo(() => meta().copyableContent())
      expect(quoted()).toContain(STAND_IN)
      await settleDiffReplies()
      const worker = onlyHeldDiffWorker(spawned)
      expect(worker.messages.map(request => request.input)).toEqual([
        { kind: 'texts', oldText: 'a\nb\nc\n', newText: 'a\nB\nc\n' },
      ])

      await answerHeldDiffRequestsUntilIdle(worker, 10)

      expect(quoted()).toContain(FINAL)
      expect(quoted()).not.toContain(STAND_IN)
      expect(meta().hasCopyable).toBe(true)
      dispose()
    })
  })

  it('asks for every diff of a declined row with two changes, whose title and body draw none', async () => {
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, toolCallFixture, toolCallMeta, toolRow } = await load()
    const row = toolRow(toolCallFixture('edit', {
      status: 'declined',
      request: {
        changes: [
          change('/p/a.ts'),
          change('/p/b.ts'),
        ],
      },
    }), 'request')

    await createRoot(async (dispose) => {
      const quoted = createMemo(() => toolCallMeta(row).copyableContent())
      expect(quoted()).toContain(STAND_IN)
      await settleDiffReplies()
      const worker = onlyHeldDiffWorker(spawned)

      // Both changes hold equal sides, so the client asks the worker once for both.
      await answerHeldDiffRequestsUntilIdle(worker, 10)

      expect(worker.messages).toHaveLength(1)
      expect(quoted()).toContain(`--- a//p/a.ts\n+++ b//p/a.ts\n@@ -1,3 +1,3 @@\n${FINAL}`)
      expect(quoted()).toContain(`--- a//p/b.ts\n+++ b//p/b.ts\n@@ -1,3 +1,3 @@\n${FINAL}`)
      dispose()
    })
  })
})
