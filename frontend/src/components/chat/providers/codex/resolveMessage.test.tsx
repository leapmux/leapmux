import type { ParsedMessageContent } from '~/lib/messageParser'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { MESSAGE_SUPPLEMENT_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { classifyMessage } from '../../messageClassification'
import { renderMessageContent } from '../../rowRenderers'
import { resolveMessageForRendering } from '../registry'
import { resolveCodexMessage } from './resolveMessage'
import '../testMocks'
import './plugin'

const STARTED = {
  threadId: 'main-thread',
  turnId: 'turn-1',
  item: { type: 'commandExecution', id: 'command-1', status: 'inProgress', command: 'printf partial' },
}

function row(supplement?: unknown): ParsedMessageContent {
  return { rawText: '', topLevel: STARTED, parentObject: STARTED, wrapper: null, supplementalContent: supplement }
}

describe('resolveCodexMessage', () => {
  it('puts the joined output back on the item it names', () => {
    expect(resolveCodexMessage(row({ itemId: 'command-1', itemType: 'commandExecution', aggregatedOutput: 'partial output' })))
      .toEqual({ ...STARTED, item: { ...STARTED.item, aggregatedOutput: 'partial output' } })
  })

  it('leaves the original object untouched', () => {
    resolveCodexMessage(row({ itemId: 'command-1', itemType: 'commandExecution', aggregatedOutput: 'partial output' }))
    expect(STARTED.item).not.toHaveProperty('aggregatedOutput')
  })

  it.each([
    ['no supplement', undefined],
    ['a non-object supplement', 'partial output'],
    ['an empty output', { itemId: 'command-1', itemType: 'commandExecution', aggregatedOutput: '' }],
    ['another item id', { itemId: 'command-2', itemType: 'commandExecution', aggregatedOutput: 'other' }],
    ['another item type', { itemId: 'command-1', itemType: 'fileChange', aggregatedOutput: 'other' }],
  ])('returns the original for %s', (_label, supplement) => {
    expect(resolveCodexMessage(row(supplement))).toBe(STARTED)
  })

  it('returns the original when the row carries no item', () => {
    const parsed: ParsedMessageContent = {
      rawText: '',
      topLevel: null,
      parentObject: { threadId: 'main-thread' },
      wrapper: null,
      supplementalContent: { itemId: 'command-1', itemType: 'commandExecution', aggregatedOutput: 'partial' },
    }
    expect(resolveCodexMessage(parsed)).toBe(parsed.parentObject)
  })
})

describe('a codex tool row whose output the worker recovered', () => {
  // The renderer must read the RESOLVED payload: the category alone states none of
  // the recovered output, so the row's body reads it from the resolved parent object.
  it('renders the recovered output rather than an empty result', () => {
    const message = makeMessage({
      agentProvider: AgentProvider.CODEX,
      completion: MessageCompletion.INTERRUPTED,
      content: rawContent(STARTED),
      supplementalContent: rawContent({
        [MESSAGE_SUPPLEMENT_FIELD.Provider]: {
          itemId: 'command-1',
          itemType: 'commandExecution',
          aggregatedOutput: 'Running 240 tests',
        },
      }),
    })
    const parsed = resolveMessageForRendering(parseMessageContent(message), AgentProvider.CODEX)
    const category = classifyMessage({ ...parsed, agentProvider: AgentProvider.CODEX })
    const { container } = render(() => renderMessageContent(
      parsed.parentObject,
      { premeasureMode: true, sources: testMessageSources({ current: () => parsed }) },
      category,
      AgentProvider.CODEX,
      message.completion,
    ))
    expect(container.textContent).toContain('Running 240 tests')
    expect(container.textContent).not.toContain('[no output]')
  })
})
