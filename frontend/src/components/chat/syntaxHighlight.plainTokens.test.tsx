import type { TokenizeRequest, TokenizeResponse } from '~/lib/shikiWorker'
import type { CapturingWorker } from '~/test-support/fakeWorker'
import { render } from '@solidjs/testing-library'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { installCapturingWorkers, settleWorkerReplies } from '~/test-support/fakeWorker'
import { CommandHighlightHtml } from './syntaxHighlight'

// ---------------------------------------------------------------------------
// The answer of the Shiki worker for code that it cannot tokenize
//
// The worker answers null tokens for a language whose grammar it cannot load.
// The real worker loads the grammar of every language that these components
// ask for, so this file answers each request by hand, with the null tokens of
// that reply. The real worker client and the real token hook take the answer.
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

describe('CommandHighlightHtml', () => {
  it('renders raw text when the worker answers null tokens (unknown or failed grammar)', async () => {
    const { container } = render(() => <CommandHighlightHtml code="echo hi" />)

    expect(spawned).toHaveLength(1)
    const worker = spawned[0]!
    expect(worker.messages.map(({ lang, code }) => [lang, code])).toEqual([
      ['bash', 'echo hi'],
    ])
    worker.reply({
      type: 'tokenize-result',
      id: worker.messages[0]!.id,
      tokens: null,
    } satisfies TokenizeResponse)
    await settleWorkerReplies()

    expect(container.querySelector('[data-shiki-token]')).toBeNull()
    expect(container.textContent).toBe('echo hi')
  })
})
