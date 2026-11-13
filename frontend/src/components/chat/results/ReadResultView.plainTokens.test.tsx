import type { TokenizeRequest, TokenizeResponse } from '~/lib/shikiWorker'
import type { CapturingWorker } from '~/test-support/fakeWorker'
import { render } from '@solidjs/testing-library'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { installCapturingWorkers, settleWorkerReplies } from '~/test-support/fakeWorker'
import { ReadResultView } from './ReadResultView'

// ---------------------------------------------------------------------------
// The answer of the Shiki worker for code that it cannot tokenize
//
// The worker answers null tokens for a language whose grammar it cannot load.
// The real worker loads the TypeScript grammar that this view asks for, so this
// file answers each request by hand, with the null tokens of that reply. The real
// worker client and the real token hook take the answer.
//
// The client spawns its worker at its first request and keeps it for the whole
// file, so the stub must be in place before any test of the file renders code.
// ---------------------------------------------------------------------------

let spawned: CapturingWorker<TokenizeRequest>[] = []

beforeAll(() => {
  spawned = installCapturingWorkers<TokenizeRequest>()
})

afterAll(() => {
  vi.unstubAllGlobals()
})

describe('ReadResultView', () => {
  it('falls back to plain text when worker tokenization returns null', async () => {
    const { container } = render(() => (
      <ReadResultView
        lines={[
          { num: 1, text: 'const x = 1' },
        ]}
        filePath="example.ts"
      />
    ))

    expect(spawned).toHaveLength(1)
    const worker = spawned[0]!
    expect(worker.messages.map(({ lang, code }) => [lang, code])).toEqual([
      ['typescript', 'const x = 1'],
    ])
    worker.reply({
      type: 'tokenize-result',
      id: worker.messages[0]!.id,
      tokens: null,
    } satisfies TokenizeResponse)
    await settleWorkerReplies()

    expect(container.textContent).toContain('const x = 1')
    expect(container.querySelector('[data-shiki-token]')).toBeNull()
  })
})
