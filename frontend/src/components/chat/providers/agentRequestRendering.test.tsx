import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { MIMO_ACTOR_ACTION, MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { renderMessageContent, renderPendingMessageContent } from '~/test-support/messageContentRenderer'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { openingFrame, toolFrame } from '~/test-support/mimoFixtures'
import { openCodeToolPart } from './opencode/toolResults.fixtures'
import { providerFor } from './registry'
import { input } from './testUtils'
import './index'

describe.each([
  AgentProvider.CLAUDE_CODE,
  AgentProvider.ZCODE,
  AgentProvider.OPENCODE,
  AgentProvider.KILO,
  AgentProvider.MIMO_CODE,
])('shared agent request (%s)', (provider) => {
  function launch() {
    const args = { description: 'Inspect project structure', subagent_type: 'explore', prompt: '**Instruction**\n\n1. Read the entry points.' }
    // MiMo's `actor` tool states the launch as one operation, and `run` waits for the
    // subagent's report.
    const mimoArgs = {
      operation: { action: MIMO_ACTOR_ACTION.Run, ...args },
    }
    const nativeArgs = { description: args.description, agent: 'explore', prompt: args.prompt }
    const request = provider === AgentProvider.OPENCODE
      ? openCodeToolPart('call', 'subagent', nativeArgs, { status: 'running' })
      : provider === AgentProvider.CLAUDE_CODE
        ? {
            type: 'assistant',
            message: {
              content: [
                {
                  type: 'tool_use',
                  id: 'call',
                  name: 'Agent',
                  input: args,
                },
              ],
            },
          }
        : provider === AgentProvider.ZCODE
          ? {
              type: 'tool.updated',
              payload: {
                kind: 'scheduled',
                toolCallId: 'call',
                toolName: 'Agent',
                input: args,
              },
            }
          : provider === AgentProvider.MIMO_CODE
            ? openingFrame(MIMO_TOOL.Actor, mimoArgs, 'call')
            : {
                sessionUpdate: 'tool_call',
                toolCallId: 'call',
                title: 'task',
                kind: 'think',
                status: 'pending',
                rawInput: args,
              }
    // Each dialect states the SAME completion: the call ended and it reported "Done".
    // The Agent Client Protocol frame carries that word in its content blocks, and it
    // has to carry it -- a completed call states a result (invariant I2), so a frame
    // that reports nothing at all describes no answered call.
    const result = provider === AgentProvider.OPENCODE
      ? openCodeToolPart('call', 'subagent', nativeArgs, {
          status: 'completed',
          content: [
            { type: 'text', text: 'Done' },
          ],
        })
      : provider === AgentProvider.CLAUDE_CODE
        ? {
            type: 'user',
            message: {
              content: [
                { type: 'tool_result', tool_use_id: 'call', content: 'Done' },
              ],
            },
          }
        : provider === AgentProvider.ZCODE
          ? {
              type: 'tool.updated',
              payload: {
                kind: 'result',
                toolCallId: 'call',
                result: { success: true, content: 'Done' },
              },
            }
          : provider === AgentProvider.MIMO_CODE
            ? toolFrame(MIMO_TOOL.Actor, { input: mimoArgs, output: 'Done' }, 'call')
            : {
                sessionUpdate: 'tool_call_update',
                toolCallId: 'call',
                status: 'completed',
                content: [
                  {
                    type: 'content',
                    content: { type: 'text', text: 'Done' },
                  },
                ],
              }
    const sources = testMessageSources({
      current: () => input(request, null, provider),
      role: () => 'request',
      visibleRows: () => ({ request: true }),
    })
    const category = providerFor(provider)!.transcript.classify(input(request, null, provider))
    return {
      request,
      result,
      renderRow: () => render(() => renderMessageContent(request, { premeasureMode: true, spanId: 'call', sources }, category, provider)),
      renderPending: () => render(() => renderPendingMessageContent(request, { premeasureMode: true, spanId: 'call', sources }, category, provider)),
      renderResult: () => render(() => renderMessageContent(result, {
        premeasureMode: true,
        spanId: 'call',
        sources: testMessageSources({
          current: () => ({
            ...input(result, null, provider),
            completion: MessageCompletion.COMPLETE,
          }),
          request: () => input(request, null, provider),
          role: () => 'result',
          visibleRows: () => ({ request: true }),
        }),
      }, providerFor(provider)!.transcript.classify(input(result, null, provider)), provider)),
    }
  }

  // While the subagent runs, the PENDING row below the request row shows the prompt.
  it('shows the pending prompt as formatted Markdown on the pending row', async () => {
    const { renderRow, renderPending } = launch()
    const row = renderRow()
    const pending = renderPending()
    await settleWorkerReplies()
    expect(row.container.textContent).toContain('Inspect project structure (explore)')
    expect(pending.container.querySelector('strong')?.textContent).toBe('Instruction')
    expect(pending.container.querySelector('ol li')?.textContent).toContain('Read the entry points.')
  })

  // No row reads a later message of its span: the request row never waits for its result.
  it('never asks for the result of a request row', () => {
    const { request } = launch()
    expect(providerFor(provider)!.transcript.needsRequest?.(input(request)) ?? false).toBe(false)
  })

  // The result row draws what the subagent reported, and no prompt: the request row
  // above it states the launch.
  it('draws the report on the result row', async () => {
    const { container } = launch().renderResult()
    await settleWorkerReplies()
    expect(container.textContent).toContain('Done')
    expect(container.textContent).not.toContain('Instruction')
  })

  // The request row draws the same before and after the result lands, so it is compact,
  // and the reader opens the prompt on it.
  it('keeps the request row compact and lets the user open its prompt', async () => {
    const { renderRow } = launch()
    const { container, getByRole } = renderRow()
    await settleWorkerReplies()
    expect(container.textContent).not.toContain('Instruction')
    await fireEvent.click(getByRole('button', { name: 'Show prompt' }))
    expect(container.querySelector('strong')?.textContent).toBe('Instruction')
  })
})
