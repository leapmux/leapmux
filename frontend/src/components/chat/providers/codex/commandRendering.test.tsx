import type { MessageCategory } from '../../messageClassifier'
import type { MessageContentRenderContext } from '../../messageContentRenderer'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { dismissActiveTooltip, SHOW_DELAY_MS } from '~/components/common/Tooltip'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { _resetTokenCache } from '~/lib/tokenCache'
import { recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { pngBase64 } from '~/test-support/pngFixture'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import { renderMessageContent } from '../../messageContentRenderer'
import './plugin'

// Each test reads which code its rows send to the Shiki worker, and a cached
// token answer sends nothing.
beforeEach(() => {
  _resetTokenCache()
})

afterEach(() => {
  dismissActiveTooltip()
  vi.useRealTimers()
})

/** The language and the code of each tokenize request that reaches the Shiki worker from now on. */
function recordTokenizeRequests(): () => Array<[string, string]> {
  const requests = recordWorkerRequests('lib/shikiWorker.ts')
  return () => requests.map(({ lang, code }) => [lang, code])
}

vi.mock('~/context/PreferencesContext', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/context/PreferencesContext')>()
  return {
    ...actual,
    usePreferences: () => ({
      diffView: () => 'unified',
      expandAgentThoughts: () => true,
    }),
  }
})

function renderCodexItem(item: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const category: MessageCategory = { kind: 'tool_use' }
  return render(() => <>{renderMessageContent(parsed, context, category, AgentProvider.CODEX)}</>)
}

describe('codex native exec rendering', () => {
  it.each([false, true])('renders the exact native result and its failure state: %s', async (failed) => {
    const request = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: {
        type: 'custom_tool_call',
        call_id: 'native-exec',
        name: 'exec',
        input: failed ? 'throw new Error("Native thrown: " + (70 + 7));' : 'text("Native computed: " + (40 + 2));',
        status: 'completed',
      },
    }
    const expected = failed ? 'Error: Native thrown: 77' : 'Native computed: 42'
    const result = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: {
        type: 'custom_tool_call_output',
        call_id: 'native-exec',
        output: [
          { type: 'input_text', text: `Script ${failed ? 'failed' : 'completed'}\nWall time 0.0 seconds\nOutput:\n` },
          { type: 'input_text', text: expected },
        ],
      },
    }
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage({
          id: 'native-request',
          provider: AgentProvider.CODEX,
          spanId: 'native-exec',
          spanType: 'exec',
          agentSessionId: 'native-session',
          content: request,
        }, 1n),
        makeTranscriptMessage({
          id: 'native-result',
          provider: AgentProvider.CODEX,
          spanId: 'native-exec',
          spanType: 'exec',
          agentSessionId: 'native-session',
          content: result,
        }, 2n),
      ],
    })
    const { container } = scenario.renderBubble('native-result')
    await waitFor(() => expect(container).toHaveTextContent(expected))
    expect(scenario.toolRow('native-result').call.status).toBe(failed ? 'failed' : 'completed')
    expect(container.textContent).not.toContain('custom_tool_call_output')
  })

  it('renders one decoded image from native exec output', async () => {
    const data = pngBase64(12, 8)
    const request = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: {
        type: 'custom_tool_call',
        call_id: 'native-image-exec',
        name: 'exec',
        input: 'image(nativeImage);',
        status: 'completed',
      },
    }
    const result = {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: {
        type: 'custom_tool_call_output',
        call_id: 'native-image-exec',
        output: [
          { type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' },
          { type: 'input_image', image_url: `data:image/png;base64,${data}` },
        ],
      },
    }
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage({
          id: 'native-image-request',
          provider: AgentProvider.CODEX,
          spanId: 'native-image-exec',
          spanType: 'exec',
          agentSessionId: 'native-session',
          content: request,
        }, 1n),
        makeTranscriptMessage({
          id: 'native-image-result',
          provider: AgentProvider.CODEX,
          spanId: 'native-image-exec',
          spanType: 'exec',
          agentSessionId: 'native-session',
          content: result,
        }, 2n),
      ],
    })
    const { container } = scenario.renderBubble('native-image-result')
    await waitFor(() => expect(container.querySelectorAll('img')).toHaveLength(1))
    expect(scenario.toolRow('native-image-result').call.images).toMatchObject([
      { url: `data:image/png;base64,${data}` },
    ])
    expect(container.textContent).not.toContain(data)
  })
})

describe('codex command actions', () => {
  it('limits collapsed action rendering and builds rich tooltips only on demand', async () => {
    vi.useFakeTimers()
    const tokenized = recordTokenizeRequests()
    const commandActions = Array.from({ length: 20 }, (_, index) => ({
      type: 'read',
      command: `sed -n '${index + 1}p' src/file.ts`,
      name: 'file.ts',
      path: '/repo/src/file.ts',
    }))
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'compound command',
      status: 'inProgress',
      commandActions,
    }, { workingDir: '/repo' })

    expect(container.querySelectorAll('[data-command-action]').length).toBeLessThanOrEqual(3)
    expect(container).toHaveTextContent('18 actions omitted')
    expect(tokenized()).toEqual([])
    expect(container.querySelector('[aria-label="Show all actions"]')).not.toBeNull()

    const firstAction = container.querySelector('[data-command-action="read"]')
    expect(firstAction).not.toBeNull()
    if (!firstAction)
      throw new Error('The collapsed command row has no read action.')
    fireEvent.mouseEnter(firstAction)
    expect(tokenized()).toEqual([])
    await vi.advanceTimersByTimeAsync(SHOW_DELAY_MS)
    expect(tokenized()).toEqual([
      ['bash', 'sed -n \'1p\' src/file.ts'],
    ])
  })

  it('renders structured actions and the process identifier through the shared execute renderer', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: '/bin/zsh -lc "sed and rg"',
      cwd: '/repo',
      processId: '79860',
      status: 'inProgress',
      commandActions: [
        {
          type: 'read',
          command: 'sed -n \'1,5p\' src/main.ts',
          name: 'main.ts',
          path: '/repo/src/main.ts',
        },
        {
          type: 'search',
          command: 'rg -n \'needle\' src',
          query: 'needle',
          path: 'src',
        },
      ],
    }, { workingDir: '/repo' })

    expect(container).toHaveTextContent('Read src/main.ts')
    expect(container).toHaveTextContent('Search for "needle" in src')
    expect(container).toHaveTextContent('Process ID:')
    expect(container).toHaveTextContent('79860')
  })

  it('syntax-highlights a raw command that an unknown action draws', async () => {
    const tokenized = recordTokenizeRequests()
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'compound command',
      status: 'inProgress',
      commandActions: [
        { type: 'unknown', command: 'printf visible-action' },
      ],
    })

    expect(tokenized()).toEqual([
      ['bash', 'printf visible-action'],
    ])
    await settleWorkerReplies()
    // Shiki colours the command word apart from its argument.
    const command = [
      ...container.querySelectorAll('[data-command-action] span'),
    ]
      .find(span => span.textContent === 'printf')
    expect(command?.className).toMatch(/^sk-/)
  })

  it('puts one known action in the title with a highlighted command tooltip', async () => {
    vi.useFakeTimers()
    const tokenized = recordTokenizeRequests()
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'compound command',
      status: 'inProgress',
      commandActions: [
        {
          type: 'read',
          command: 'sed -n \'1,5p\' src/main.ts',
          name: 'main.ts',
          path: '/repo/src/main.ts',
        },
      ],
    }, { workingDir: '/repo' })
    const title = container.querySelector('[data-testid="execute-title"]')

    expect(title).toHaveTextContent('Read src/main.ts')
    expect(container.querySelector('[data-command-action]')).toBeNull()
    expect(title).not.toBeNull()
    if (!title)
      throw new Error('The command row has no execute title.')
    fireEvent.mouseEnter(title)
    expect(tokenized()).toEqual([])
    await vi.advanceTimersByTimeAsync(SHOW_DELAY_MS)
    expect(tokenized()).toEqual([
      ['bash', 'sed -n \'1,5p\' src/main.ts'],
    ])
  })
})
