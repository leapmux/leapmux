import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import './testMocks'

const { renderMessageContent } = await import('../messageRenderers')

function subagentRow(over: Partial<BackgroundTaskItem> = {}): BackgroundTaskItem {
  return {
    rowKey: 'a1b2c3d4e5f60718',
    kind: 'subagent',
    childAgentId: 'child-1',
    title: 'Explore the parser',
    activity: '',
    status: 'completed',
    ...over,
  }
}

function renderToolUse(input: Record<string, unknown>, context?: RenderContext): HTMLElement {
  const msg = {
    type: 'assistant',
    message: {
      content: [{ type: 'tool_use', id: 'test-sendmessage', name: 'SendMessage', input }],
    },
  }
  const content = msg.message.content as Array<Record<string, unknown>>
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: 'SendMessage',
    toolUse: content[0],
    content,
  }
  const result = renderMessageContent(msg, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result).container
}

describe('claude SendMessage tool_use rendering', () => {
  // The recipient is what the card exists to show. The generic fallback title
  // cannot: `to` is not among the input keys it inspects, so it rendered the
  // message body and left the addressee invisible.
  it('leads with the recipient and shows the message under it', () => {
    const container = renderToolUse({ to: 'a1b2c3d4e5f60718', message: 'keep going' })
    const text = container.textContent ?? ''
    expect(text).toContain('a1b2c3d4e5f60718')
    expect(text).toContain('keep going')
    expect(text.indexOf('a1b2c3d4e5f60718')).toBeLessThan(text.indexOf('keep going'))
  })

  // The summary line has no height cap, so a long steering message would
  // otherwise inflate the chat row it sits in.
  it('clips a long message to one line', () => {
    const long = `${'x'.repeat(200)}\nsecond line`
    const text = renderToolUse({ to: 'a1b2c3d4e5f60718', message: long }).textContent ?? ''
    expect(text).toContain('\u2026')
    expect(text).not.toContain('second line')
    expect(text).not.toContain('x'.repeat(200))
  })

  // The structured message kinds (shutdown_request, plan_approval_response) are
  // objects with no one-line form; the card shows the recipient alone.
  it('shows no summary for a structured message', () => {
    const text = renderToolUse({ to: 'a1b2c3d4e5f60718', message: { kind: 'shutdown_request' } }).textContent ?? ''
    expect(text).toContain('a1b2c3d4e5f60718')
    expect(text).not.toContain('shutdown_request')
  })

  it('shows the recipient the way the Background tasks list shows it', () => {
    const container = renderToolUse(
      { to: 'a1b2c3d4e5f60718', message: 'keep going' },
      { resolveBackgroundTaskRow: () => subagentRow(), onOpenSubagent: vi.fn() },
    )
    expect(container.textContent).toContain('Explore the parser')
  })

  it('opens the subagent tab when the recipient identifies a row that owns one', () => {
    const onOpenSubagent = vi.fn()
    const row = subagentRow()
    const container = renderToolUse(
      { to: 'a1b2c3d4e5f60718', message: 'keep going' },
      { resolveBackgroundTaskRow: () => row, onOpenSubagent },
    )
    const button = container.querySelector<HTMLButtonElement>('[data-testid="send-message-recipient"]')
    expect(button).not.toBeNull()
    button!.click()
    expect(onOpenSubagent).toHaveBeenCalledWith(row)
  })

  // A display name, another session, a uds:/bridge:/did: address: none of these
  // identify a row here, so the card must not pretend to be somewhere to go.
  it('renders an unresolvable recipient as plain text', () => {
    const container = renderToolUse(
      { to: 'bridge:another-machine', message: 'keep going' },
      { resolveBackgroundTaskRow: () => undefined, onOpenSubagent: vi.fn() },
    )
    expect(container.querySelector('[data-testid="send-message-recipient"]')).toBeNull()
    expect(container.textContent).toContain('bridge:another-machine')
  })

  // A shell row has no transcript, and a subagent row whose provider never
  // linked one has nothing to open either.
  it('is not clickable for a row that owns no transcript', () => {
    for (const row of [subagentRow({ childAgentId: undefined }), subagentRow({ kind: 'shell' })]) {
      const container = renderToolUse(
        { to: 'a1b2c3d4e5f60718', message: 'keep going' },
        { resolveBackgroundTaskRow: () => row, onOpenSubagent: vi.fn() },
      )
      expect(container.querySelector('[data-testid="send-message-recipient"]')).toBeNull()
    }
  })

  // Without a handler there is nowhere to send the click, so the label must not
  // pose as a control.
  it('is not clickable when the host supplied no open handler', () => {
    const container = renderToolUse(
      { to: 'a1b2c3d4e5f60718', message: 'keep going' },
      { resolveBackgroundTaskRow: () => subagentRow() },
    )
    expect(container.querySelector('[data-testid="send-message-recipient"]')).toBeNull()
    expect(container.textContent).toContain('Explore the parser')
  })

  it('falls back to the generic card when the call gives no recipient', () => {
    const container = renderToolUse({ message: 'keep going' })
    expect(container.textContent).toContain('SendMessage')
  })
})
