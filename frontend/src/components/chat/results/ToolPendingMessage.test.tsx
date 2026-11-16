import type { MessageUiKey } from '../messageUiKeys'
import type { ToolResultRenderContext } from '../renderContext'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeAll, describe, expect, it } from 'vitest'
import { classSelector } from '~/test-support/composedClass'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { toolPendingShown } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { ToolMessage } from './ToolMessage'
import { toolCallHasPending, ToolPendingMessage } from './ToolPendingMessage'

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
      progress={{
        liveTail: () => outputTail === undefined
          ? undefined
          : {
              outputTail,
              ...(outputTruncated
                ? { outputTruncated }
                : {}),
            },
      }}
    />
  ))
  return container.querySelector('[data-testid="tool-pending"]')?.textContent ?? ''
}

describe('toolCallHasPending', () => {
  it('answers true for live output, whatever the kind previews', () => {
    expect(toolCallHasPending(runningCommand, () => false, 'compiling')).toBe(true)
  })

  it('answers false for a call with no preview and no live output', () => {
    expect(toolCallHasPending(runningCommand, () => false, undefined)).toBe(false)
  })

  // The Worker sends an empty tail before the first byte; it draws nothing.
  it('answers false for an empty live output', () => {
    expect(toolCallHasPending(runningCommand, () => false, '')).toBe(false)
  })

  // The pending row and the request row share one expand state. An agent's prompt is on
  // the pending row while collapsed, and on the request row once expanded.
  it('reads the expand key that the row reads', () => {
    const asked: MessageUiKey[] = []
    const expandedOf = (expanded: boolean) => (key: MessageUiKey): boolean => {
      asked.push(key)
      return expanded
    }
    expect(toolCallHasPending(runningAgent, expandedOf(false), undefined)).toBe(true)
    expect(toolCallHasPending(runningAgent, expandedOf(true), undefined)).toBe(false)
    expect(asked).toEqual([MESSAGE_UI_KEY.AGENT_PROMPT, MESSAGE_UI_KEY.AGENT_PROMPT])
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
        progress={{
          liveTail: () => {
            const tail = outputTail()
            return tail === undefined
              ? undefined
              : { outputTail: tail }
          },
        }}
      />
    ))
    const root = () => container.querySelector('[data-testid="tool-pending"]')!
    expect(root().matches(classSelector(toolPendingShown))).toBe(false)
    expect(root().innerHTML).toBe('')
    setOutputTail('compiling')
    expect(root().matches(classSelector(toolPendingShown))).toBe(true)
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
