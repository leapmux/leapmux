import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { failedResult } from '~/components/chat/model/toolCall'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('think renderer', () => {
  checkKindModule({
    kind: 'think',
    request: { text: 'consider the options' },
    titlePart: 'RAW TITLE',
    result: { text: 'a thought', format: 'plain' },
    resultPart: 'a thought',
  })

  // The pending row leaves when the result row joins the list. A call that ended with no
  // answer states the first line of its thought on the row that ended it.
  describe('a call that ended with no answer', () => {
    const resultText = (call: ReturnType<typeof toolCallFixture>) => render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />).container.textContent ?? ''

    it('draws the first line of the thought', () => {
      const call = toolCallFixture('think', {
        status: 'cancelled',
        request: { text: 'FIRST-LINE of the thought\nsecond line' },
      })
      const text = resultText(call)
      expect(text).toContain('FIRST-LINE of the thought')
      expect(text).not.toContain('second line')
    })

    it('draws no line beside an answer in words', () => {
      const call = toolCallFixture('think', {
        status: 'failed',
        request: { text: 'FIRST-LINE of the thought' },
        result: failedResult('The model stopped thinking.'),
      })
      expect(resultText(call)).not.toContain('FIRST-LINE')
    })
  })
})
