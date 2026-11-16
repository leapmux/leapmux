import type { MessageUiKey } from '../messageUiKeys'
import type { ToolResultRenderContext } from '../renderContext'
import { render } from '@solidjs/testing-library'
import { batch, createSignal } from 'solid-js'
import { beforeAll, describe, expect, it } from 'vitest'
import { classSelector } from '~/test-support/composedClass'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolPending } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { ToolMessage } from './ToolMessage'
import { pendingRowRequest, ToolPendingMessage } from './ToolPendingMessage'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

/** A running command, whose kind previews nothing of its own while it runs. */
const runningCommand = toolCallFixture('execute', {
  status: 'in_progress',
  request: { command: 'make' },
})

/** A running subagent with a prompt, which its pending row previews while collapsed. */
const runningAgent = toolCallFixture('agent', {
  status: 'in_progress',
  request: { description: 'Fix the build', prompt: 'Run the tests.' },
})

/** The text that the pending row of the running command draws, with the live tail `outputTail`. */
function pendingText(outputTail: string | undefined, outputTruncated = false): string {
  const { container } = render(() => (
    <ToolPendingMessage
      row={toolRow(runningCommand, 'request')}
      context={{
        toolProgress: {
          liveTail: () => outputTail === undefined
            ? undefined
            : {
                outputTail,
                ...(outputTruncated
                  ? { outputTruncated }
                  : {}),
              },
        },
      }}
    />
  ))
  return container.querySelector('[data-testid="tool-pending"]')?.textContent ?? ''
}

// The list places a pending row after a request row alone (see chatPendingRows), so the
// bubble and the isolated renderer draw one for a request row alone.
describe('pendingRowRequest', () => {
  it('answers the row of a tool request', () => {
    const row = toolRow(runningCommand, 'request')
    expect(pendingRowRequest(row)).toBe(row)
  })

  it('answers null for an update row, a result row, a row of another kind, and no row', () => {
    expect(pendingRowRequest(toolRow(runningCommand, 'update'))).toBeNull()
    expect(pendingRowRequest(toolRow(runningCommand, 'result', { request: true }))).toBeNull()
    expect(pendingRowRequest({ kind: 'assistant-text', text: 'x' })).toBeNull()
    expect(pendingRowRequest(null)).toBeNull()
  })
})

describe('ToolPendingMessage', () => {
  it('draws the live output without its leading blank lines', () => {
    expect(pendingText('\n\ncompiling main.c')).toBe('compiling main.c')
  })

  it('states the truncation notice when the Worker kept only the tail of the output', () => {
    expect(pendingText('the last line', true)).toContain(TRUNCATION_NOTICE)
    expect(pendingText('the last line')).not.toContain(TRUNCATION_NOTICE)
  })

  it('draws nothing for a call with no preview and no live output', () => {
    expect(pendingText(undefined)).toBe('')
  })

  // The list leaves no gap above a pending row, so an empty one takes no space at all. The
  // row takes that space back as its own top padding once it draws something.
  it('takes its top space only while it draws something', () => {
    const [outputTail, setOutputTail] = createSignal<string | undefined>(undefined)
    const { container } = render(() => (
      <ToolPendingMessage
        row={toolRow(runningCommand, 'request')}
        context={{
          toolProgress: {
            liveTail: () => {
              const tail = outputTail()
              return tail === undefined
                ? undefined
                : { outputTail: tail }
            },
          },
        }}
      />
    ))
    const root = () => container.querySelector('[data-testid="tool-pending"]')!
    expect(root().matches(classSelector(toolPending))).toBe(true)
    expect(root().matches(':empty')).toBe(true)
    setOutputTail('compiling')
    expect(root().matches(':empty')).toBe(false)
  })

  // The Worker can broadcast a tail of line breaks before the first printed line. The row
  // strips the leading blank lines, so such a tail draws nothing and takes no space.
  it('takes no space for a live output of blank lines alone', () => {
    const { container } = render(() => (
      <ToolPendingMessage
        row={toolRow(runningCommand, 'request')}
        context={{
          toolProgress: {
            liveTail: () => ({ outputTail: '\n\n' }),
          },
        }}
      />
    ))
    const root = container.querySelector('[data-testid="tool-pending"]')!
    expect(root.matches(':empty')).toBe(true)
    expect(root.innerHTML).toBe('')
  })

  // The store drops the live output the moment the result message arrives, but the row
  // stays until the result row is measured and replaces it in one update. Keeping the last
  // output keeps the row's height, so the swap moves nothing.
  it('keeps the output it drew while the result of its call is held', () => {
    const [outputTail, setOutputTail] = createSignal<string | undefined>('compiling')
    const [resultHeld, setResultHeld] = createSignal(false)
    const { container } = render(() => (
      <ToolPendingMessage
        row={toolRow(runningCommand, 'request')}
        resultHeld={resultHeld}
        context={{
          toolProgress: {
            liveTail: () => {
              const tail = outputTail()
              return tail === undefined
                ? undefined
                : { outputTail: tail }
            },
          },
        }}
      />
    ))
    const root = () => container.querySelector('[data-testid="tool-pending"]')!
    expect(root().textContent).toBe('compiling')

    batch(() => {
      setResultHeld(true)
      setOutputTail(undefined)
    })
    expect(root().textContent).toBe('compiling')
    expect(root().matches(':empty')).toBe(false)

    // The browser can release the held result, as when the window moves off the tail.
    // The output that the row kept for it goes with it.
    setResultHeld(false)
    expect(root().textContent).toBe('')
    expect(root().matches(':empty')).toBe(true)
  })

  // A turn that ends clears the output of a call that never answered. No result row
  // replaces this row, so it keeps no output that the store no longer holds.
  it('drops the output that the store clears while no result is held', () => {
    const [outputTail, setOutputTail] = createSignal<string | undefined>('compiling')
    const { container } = render(() => (
      <ToolPendingMessage
        row={toolRow(runningCommand, 'request')}
        resultHeld={() => false}
        context={{
          toolProgress: {
            liveTail: () => {
              const tail = outputTail()
              return tail === undefined
                ? undefined
                : { outputTail: tail }
            },
          },
        }}
      />
    ))
    setOutputTail(undefined)
    expect(container.querySelector('[data-testid="tool-pending"]')!.innerHTML).toBe('')
  })

  // One Expand control, on the request row, opens both rows of the call: the prompt moves
  // from the pending row to the request row, so it is drawn once.
  it('shares the expand state of the request row', async () => {
    const [state, setState] = createSignal<ReadonlyMap<MessageUiKey, boolean>>(new Map())
    const context: ToolResultRenderContext = {
      getMessageUiState: key => state().get(key),
      setMessageUiState: (key, value) => setState(previous => new Map(previous).set(key, value)),
    }
    const { container } = render(() => (
      <>
        <div data-row="request"><ToolMessage row={toolRow(runningAgent, 'request')} context={context} /></div>
        <div data-row="pending"><ToolPendingMessage row={toolRow(runningAgent, 'request')} context={context} /></div>
      </>
    ))
    const rowText = (row: string) => container.querySelector(`[data-row="${row}"]`)?.textContent ?? ''
    await settleWorkerReplies()
    expect(rowText('request')).not.toContain('Run the tests.')
    expect(rowText('pending')).toContain('Run the tests.')

    context.setMessageUiState!(MESSAGE_UI_KEY.AGENT_PROMPT, true)
    await settleWorkerReplies()
    expect(rowText('request')).toContain('Run the tests.')
    expect(rowText('pending')).not.toContain('Run the tests.')
  })
})
