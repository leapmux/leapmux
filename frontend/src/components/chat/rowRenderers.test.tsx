import type { MessageCompletion } from './assembledMessage'
import type { ChatRow } from './model/row'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { ERROR_MARKER, INTERRUPTION_MARKER } from './assembledMessage'
import { failedResult } from './model/toolCall'
import { renderExtractedRow } from './rowRenderers'

/** The text of one extracted row, drawn with `completion`, once its Markdown landed. */
async function drawnText(row: ChatRow, completion: MessageCompletion | null): Promise<string> {
  const { container } = render(() => renderExtractedRow({ kind: 'row', row, completion }, undefined))
  await settleWorkerReplies()
  return container.textContent ?? ''
}

const INTERRUPTED_DIVIDER: ChatRow = {
  kind: 'divider',
  divider: { label: 'Turn interrupted (1.2s)' },
}

describe('renderExtractedRow', () => {
  it.each([
    ['declined', 'error', 'Declined'],
    ['declined', 'interrupted', 'Declined'],
    ['cancelled', 'error', 'Interrupted'],
    ['failed', 'error', 'Error'],
  ] as const)('preserves the effective %s tool outcome with recorded %s completion', async (status, completion, expected) => {
    const reason = 'The tool did not run.'
    const call = toolCallFixture('execute', {
      status,
      result: failedResult(reason),
    })
    const text = await drawnText(toolRow(call), completion)
    expect(text).toContain(expected)
    expect(text.match(/The tool did not run\./g)).toHaveLength(1)
    if (expected !== 'Error')
      expect(text).not.toContain('Error')
  })

  it('draws the finality marker without an error or interruption claim', async () => {
    const text = await drawnText({ kind: 'assistant-text', text: 'Native partial text' }, 'finished')
    expect(text).toContain('Native partial text')
    expect(text).toContain('Text ended without a known outcome.')
    expect(text).not.toContain(ERROR_MARKER)
    expect(text).not.toContain(INTERRUPTION_MARKER)
  })

  it('draws the interruption marker under the text that a stop cut', async () => {
    const text = await drawnText({ kind: 'assistant-text', text: 'Half a sen' }, 'interrupted')
    expect(text).toContain('Half a sen')
    expect(text).toContain(INTERRUPTION_MARKER)
  })

  it('draws the error marker under the text that a failure cut', async () => {
    expect(await drawnText({ kind: 'assistant-text', text: 'Half a sen' }, 'error')).toContain(ERROR_MARKER)
  })

  it('draws no marker under complete text', async () => {
    const text = await drawnText({ kind: 'assistant-text', text: 'Whole.' }, 'complete')
    expect(text).not.toContain(INTERRUPTION_MARKER)
    expect(text).not.toContain(ERROR_MARKER)
  })

  // The note of a steer the model echoed draws as an inline chip beside the text
  // it interrupted, not as part of the text itself.
  it('draws each steer note of a segmented text as its own chip', async () => {
    const text = await drawnText({
      kind: 'assistant-text',
      text: 'Done. probe handled',
      segments: [
        { kind: 'text', text: 'Done. ' },
        { kind: 'steer-note', steerId: 'steer-09dc', note: 'probe handled' },
      ],
    }, 'complete')
    expect(text).toContain('Done.')
    expect(text).toContain('probe handled')
    expect(text).toContain('»')
    // The steer id lives in the chip's tooltip, not in the drawn text.
    expect(text).not.toContain('steer-09dc')
    expect(text).not.toContain(INTERRUPTION_MARKER)
  })

  // A divider states the outcome of the turn in its own label. The marker states
  // truncated TEXT, so under a divider it claimed a cut that a turn with no text, or
  // with complete text, never had -- on every stopped turn of every provider whose
  // divider carries the turn's completion.
  it.each(['interrupted', 'error'] as const)('draws no text marker under a divider of a turn that ended %s', async (completion) => {
    const text = await drawnText(INTERRUPTED_DIVIDER, completion)
    expect(text).toContain('Turn interrupted (1.2s)')
    expect(text).not.toContain(INTERRUPTION_MARKER)
    expect(text).not.toContain(ERROR_MARKER)
  })
})

// A message that another agent sent states its sender above its text, where the
// user's own message states nothing, so the reader never takes it for their own.
describe('renderExtractedRow a message from another agent', () => {
  const sender = { tabId: 'sender-tab', provider: AgentProvider.CLAUDE_CODE }
  const fromAgent: ChatRow = {
    kind: 'user',
    text: 'Run the tests',
    attachments: [],
    sender,
  }

  async function drawn(row: ChatRow, tabTitle?: (tabId: string) => string | undefined) {
    const result = render(() => renderExtractedRow({ kind: 'row', row, completion: null }, tabTitle === undefined
      ? undefined
      : { tabTitle }))
    await settleWorkerReplies()
    return result
  }

  it('states the title of the sending tab and its provider above the text', async () => {
    const { getByTestId, container } = await drawn(fromAgent, id => (id === 'sender-tab'
      ? 'Fix flaky spec'
      : undefined))
    expect(getByTestId('agent-message-sender').textContent).toBe('Message from Fix flaky spec (Claude Code)')
    expect(getByTestId('agent-message-sender').querySelector('svg')).not.toBeNull()
    expect(container.textContent).toContain('Run the tests')
  })

  it('states the tab id when the browser holds no title for the sender', async () => {
    const { getByTestId } = await drawn(fromAgent, () => undefined)
    expect(getByTestId('agent-message-sender').textContent).toBe('Message from sender-tab (Claude Code)')
  })

  it('states the tab id when the row draws outside a host that holds tabs', async () => {
    const { getByTestId } = await drawn(fromAgent)
    expect(getByTestId('agent-message-sender').textContent).toBe('Message from sender-tab (Claude Code)')
  })

  it('states the sender of a message of attachments alone', async () => {
    const { getByTestId, container } = await drawn({
      ...fromAgent,
      text: '',
      attachments: [
        { filename: 'log.txt' },
      ],
    })
    expect(getByTestId('agent-message-sender')).toBeTruthy()
    expect(container.textContent).toContain('log.txt')
  })

  it('states no sender on the user\'s own message', async () => {
    const { queryByTestId, container } = await drawn({ kind: 'user', text: 'Run the tests', attachments: [] }, () => 'Fix flaky spec')
    expect(queryByTestId('agent-message-sender')).toBeNull()
    expect(container.textContent).toContain('Run the tests')
  })
})
