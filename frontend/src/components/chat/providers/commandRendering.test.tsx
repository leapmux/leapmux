import type { MessageBubbleHost } from '../MessageBubble'
import type { MessageCategory } from '../messageClassification'
import type { RenderContext } from '../messageRenderers'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import Terminal from 'lucide-solid/icons/terminal'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { MessageBubble } from '../MessageBubble'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { claudeToolResultMeta } from './claude/toolResult'
import { codexToolResultMeta } from './codex/toolResult'
import './claude'
import './codex'
import './opencode'
import './pi'
import './testMocks'

const normalizeProgressOutputCalls = vi.hoisted(() => vi.fn())
const normalizedCommandBodyCalls = vi.hoisted(() => vi.fn())
const stripToolUseHeaderFromOutputCalls = vi.hoisted(() => vi.fn())
const tokenizeAsyncCalls = vi.hoisted(() => vi.fn())
const tokenizeAsyncMock = vi.hoisted(() => vi.fn(async (lang: string, code: string) => {
  tokenizeAsyncCalls(lang, code)
  return code.split('\n').map(line => [{
    content: line,
    htmlStyle: {
      '--shiki-light': 'rgb(1, 2, 3)',
      '--shiki-dark': 'rgb(4, 5, 6)',
    },
  }])
}))
const renderAnsiCalls = vi.hoisted(() => vi.fn())

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: tokenizeAsyncMock,
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
}))

vi.mock('~/lib/renderAnsi', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/renderAnsi')>()
  return {
    ...actual,
    renderAnsi: (text: string) => {
      renderAnsiCalls(text)
      return actual.renderAnsi(text)
    },
  }
})

vi.mock('~/lib/normalizeProgressOutput', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/normalizeProgressOutput')>()
  return {
    ...actual,
    normalizeProgressOutput: (text: string) => {
      normalizeProgressOutputCalls(text)
      return actual.normalizeProgressOutput(text)
    },
    normalizedCommandBody: (text: string) => {
      normalizedCommandBodyCalls(text)
      return actual.normalizedCommandBody(text)
    },
  }
})

vi.mock('./codex/extractors/commandExecution', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./codex/extractors/commandExecution')>()
  return {
    ...actual,
    stripToolUseHeaderFromOutput: (output: string) => {
      stripToolUseHeaderFromOutputCalls(output)
      return actual.stripToolUseHeaderFromOutput(output)
    },
  }
})

vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    diffView: () => 'unified',
    expandAgentThoughts: () => true,
  }),
}))

const { renderMessageContent } = await import('../messageRenderers')
const { createMessageRenderCacheStore } = await import('../messageRenderCache')
const { BashHighlightHtml } = await import('../toolRenderers')
const { COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT } = await import('../chatHeightShared')
const { CollapsibleContent } = await import('../results/CollapsibleContent')
const { CommandInputBody, CommandInputSummary } = await import('../results/multiLineCommandBody')
const { ToolUseMessage } = await import('./claude/toolUse/genericToolUse')
const { ToolCallUpdateMessage } = await import('./acp/renderers/toolCallUpdate')
const { PiBashRenderer } = await import('./pi/renderers/toolExecution')

function renderClaudeToolResult(parsed: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function renderClaudeToolUse(toolName: string, input: Record<string, unknown>, context?: RenderContext) {
  const parsed = {
    type: 'assistant',
    message: {
      role: 'assistant',
      content: [{ type: 'tool_use', id: 'toolu_1', name: toolName, input }],
    },
  }
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName,
    toolUse: { type: 'tool_use', id: 'toolu_1', name: toolName, input },
    content: [],
  }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

function makeBashResult(toolUseResult: Record<string, unknown> | undefined, content: string, isError = false) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'r1', content, is_error: isError }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

function renderCodexItem(item: Record<string, unknown>, context?: RenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const toolName = String(item.type ?? 'codex')
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName,
    toolUse: parsed,
    content: [],
  }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

function renderCodexMessageBubble(item: Record<string, unknown>, host?: MessageBubbleHost, opts: { premeasureMode?: boolean } = {}) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const message = create(AgentChatMessageSchema, {
    id: 'cmd-msg',
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(parsed)),
    seq: 1n,
    createdAt: 'T',
    agentProvider: AgentProvider.CODEX,
    spanId: 'cmd-span',
    spanType: String(item.type ?? ''),
  })
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: String(item.type ?? 'codex'),
    toolUse: parsed,
    content: [],
  }
  return render(() => (
    <MessageBubble
      message={message}
      parsed={{ rawText: JSON.stringify(parsed), topLevel: parsed, parentObject: parsed, wrapper: null }}
      category={category}
      host={host}
      premeasureMode={opts.premeasureMode}
    />
  ))
}

function renderOpenCodeUpdate(toolUse: Record<string, unknown>, context?: RenderContext) {
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: (toolUse.kind as string) || 'tool_call_update',
    toolUse,
    content: [],
  }
  const result = renderMessageContent(toolUse, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

function mockCollapsedCommandOverflow() {
  let scrollHeight = 96
  let clientHeight = 58
  const scroll = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(() => scrollHeight)
  const client = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockImplementation(() => clientHeight)
  return {
    fit: () => {
      scrollHeight = 24
      clientHeight = 58
    },
    restore: () => {
      scroll.mockRestore()
      client.mockRestore()
    },
  }
}

async function settleCommandOverflowMeasure(): Promise<void> {
  if (typeof requestAnimationFrame === 'function')
    await new Promise(resolve => requestAnimationFrame(resolve))
  await new Promise(resolve => setTimeout(resolve, 0))
  await Promise.resolve()
}

function showFullCommandButton(container: HTMLElement): HTMLButtonElement | null {
  return container.querySelector('[aria-label="Show full command"]')
}

describe('canonical command status label across providers', () => {
  it('claude Bash with isError but no exit code renders "Error"', () => {
    const parsed = makeBashResult({ tool_name: 'Bash', stdout: '', stderr: 'oh no' }, '', true)
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('Error')
    expect(container.textContent ?? '').not.toContain('exit')
  })

  it('codex commandExecution with exit 5 renders "Error (exit 5)"', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'false',
      aggregatedOutput: 'failed',
      exitCode: 5,
      status: 'completed',
    })
    expect(container.textContent ?? '').toContain('Error (exit 5)')
  })

  it('openCode execute with exit 5 renders "Error (exit 5)"', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'execute',
      status: 'completed',
      title: 'Run something',
      rawInput: { command: 'false' },
      rawOutput: { metadata: { exit: 5 } },
      content: [{ type: 'content', content: { text: 'failed' } }],
    })
    expect(container.textContent ?? '').toContain('Error (exit 5)')
  })

  it('uses the ACP tool-call expanded key for delegated execute output', () => {
    const output = Array.from({ length: 8 }, (_, i) => `output line ${i + 1}`).join('\n')
    const [expanded, setExpanded] = createSignal(false)

    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'execute',
      status: 'completed',
      title: 'Run verbose command',
      rawInput: { command: 'echo verbose' },
      rawOutput: { metadata: { exit: 0 } },
      content: [{ type: 'content', content: { text: output } }],
    }, {
      getMessageUiState: key => key === 'opencode-tool-call-update' ? expanded() : undefined,
      setMessageUiState: (key, value) => {
        if (key === 'opencode-tool-call-update')
          setExpanded(value)
      },
    })

    expect(container.textContent ?? '').toContain('output line 1')
    expect(container.textContent ?? '').not.toContain('output line 8')

    const expandButton = container.querySelector('.lucide-unfold-vertical')?.closest('button')
    expect(expandButton).not.toBeNull()
    fireEvent.click(expandButton!)

    expect(container.textContent ?? '').toContain('output line 8')
  })
})

describe('command summary syntax highlighting selection stability', () => {
  it('renders collapsed command summaries from the full command text', async () => {
    tokenizeAsyncCalls.mockClear()
    const command = [
      'printf first-line',
      'printf second-line',
      'printf third-line',
      'printf fourth-line',
    ].join('\n')

    const { container } = render(() => (
      <CommandInputSummary
        collapsed
        command={command}
        context={{}}
        namespace="test.collapsedFullCommandSummary"
      />
    ))

    await Promise.resolve()

    expect(container.textContent).toContain('printf first-line')
    expect(container.textContent).toContain('printf fourth-line')
    expect(container.querySelector('[data-command-input-collapsed]')).not.toBeNull()
  })

  it('trims leading blank lines only in collapsed command summaries', async () => {
    const { container } = render(() => (
      <CommandInputSummary
        collapsed
        command={'\n\n  \necho real-command'}
        context={{}}
        namespace="test.leadingBlankCommandSummary"
      />
    ))

    await Promise.resolve()

    expect(container.textContent?.startsWith('echo real-command')).toBe(true)
  })

  it('marks collapsed command summaries as overflowing only when the rendered block is clipped', async () => {
    const scrollHeight = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get')
    const clientHeight = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get')
    try {
      scrollHeight.mockReturnValue(48)
      clientHeight.mockReturnValue(58)
      const fitting = render(() => (
        <CommandInputSummary
          collapsed
          command="echo short"
          context={{}}
          namespace="test.fittingCommandSummary"
        />
      ))
      await new Promise(resolve => setTimeout(resolve, 0))
      expect(fitting.container.querySelector('[data-command-input-overflowing]')).toBeNull()
      fitting.unmount()

      scrollHeight.mockReturnValue(96)
      clientHeight.mockReturnValue(58)
      const overflowing = render(() => (
        <CommandInputSummary
          collapsed
          command={'echo one\necho two\necho three\necho four'}
          context={{}}
          namespace="test.overflowingCommandSummary"
        />
      ))
      await new Promise(resolve => setTimeout(resolve, 0))
      expect(overflowing.container.querySelector('[data-command-input-overflowing]')).not.toBeNull()
      overflowing.unmount()
    }
    finally {
      scrollHeight.mockRestore()
      clientHeight.mockRestore()
    }
  })

  it('measures collapsed command overflow during premeasure', async () => {
    const scrollHeight = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get')
    const clientHeight = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get')
    try {
      scrollHeight.mockReturnValue(96)
      clientHeight.mockReturnValue(58)
      const { container } = render(() => (
        <CommandInputSummary
          collapsed
          command={'echo one\necho two\necho three\necho four'}
          context={{ premeasureMode: true }}
          namespace="test.premeasureOverflowingCommandSummary"
        />
      ))
      await new Promise(resolve => setTimeout(resolve, 0))

      expect(container.querySelector('[data-command-input-overflowing]')).not.toBeNull()
    }
    finally {
      scrollHeight.mockRestore()
      clientHeight.mockRestore()
    }
  })

  it('does not tokenize command input over the command highlight limit', async () => {
    tokenizeAsyncCalls.mockClear()
    const command = `git diff -- ${'frontend/src/components/chat/messageRenderers.tsx '.repeat(Math.ceil(COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT / 50) + 1)}`

    const { container } = render(() => (
      <CommandInputBody
        command={command}
        context={{}}
        namespace="test.longCommandBody"
      />
    ))

    await Promise.resolve()

    expect(container.textContent).toContain('git diff --')
    expect(command.length).toBeGreaterThan(COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT)
    expect(tokenizeAsyncCalls).not.toHaveBeenCalled()
    expect(container.querySelector('span[data-shiki-token]')).toBeNull()
  })

  it('renders command input as plain text while syntax highlighting is paused', async () => {
    tokenizeAsyncCalls.mockClear()

    const { container } = render(() => (
      <BashHighlightHtml
        class="summary"
        code="echo paused"
        context={{
          syntaxHighlightingPaused: () => true,
        }}
        namespace="test.pausedCommandSummary"
      />
    ))

    await Promise.resolve()

    expect(container.textContent).toContain('echo paused')
    expect(tokenizeAsyncCalls).not.toHaveBeenCalled()
  })

  it('does not apply in-flight command tokenization while syntax highlighting becomes paused', async () => {
    tokenizeAsyncCalls.mockClear()
    type TestTokens = Array<Array<{ content: string, htmlStyle: { '--shiki-light': string, '--shiki-dark': string } }>>
    let resolveTokens: ((tokens: TestTokens) => void) | undefined
    tokenizeAsyncMock.mockImplementationOnce((lang: string, code: string) => {
      tokenizeAsyncCalls(lang, code)
      return new Promise<TestTokens>((resolve) => {
        resolveTokens = resolve
      })
    })
    const [paused, setPaused] = createSignal(false)
    const { container } = render(() => (
      <BashHighlightHtml
        class="summary"
        code="echo paused"
        context={{
          syntaxHighlightingPaused: paused,
        }}
        namespace="test.inflightPausedCommandSummary"
      />
    ))

    expect(tokenizeAsyncCalls).toHaveBeenCalledWith('bash', 'echo paused')

    setPaused(true)
    resolveTokens?.([[{
      content: 'echo paused',
      htmlStyle: {
        '--shiki-light': 'rgb(9, 8, 7)',
        '--shiki-dark': 'rgb(7, 8, 9)',
      },
    }]])
    await Promise.resolve()

    expect(container.querySelector('span[data-shiki-token]')).toBeNull()

    setPaused(false)

    await waitFor(() => {
      expect(tokenizeAsyncCalls).toHaveBeenCalledTimes(2)
      expect(container.querySelector('span[data-shiki-token]')).not.toBeNull()
    })
  })

  it('tokenizes command input after an initially active text selection clears', async () => {
    tokenizeAsyncCalls.mockClear()
    const [selectionActive, setSelectionActive] = createSignal(true)

    render(() => (
      <BashHighlightHtml
        class="summary"
        code="echo selected"
        context={{ textSelectionActive: selectionActive }}
        namespace="test.selectionClearsCommandSummary"
      />
    ))

    await Promise.resolve()
    expect(tokenizeAsyncCalls).not.toHaveBeenCalled()

    setSelectionActive(false)
    await Promise.resolve()

    expect(tokenizeAsyncCalls).toHaveBeenCalledWith('bash', 'echo selected')
  })

  it('shows the full-command affordance when a short Claude Bash summary overflows after wrapping', async () => {
    const scrollHeight = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get')
    const clientHeight = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get')
    try {
      scrollHeight.mockReturnValue(96)
      clientHeight.mockReturnValue(58)

      const { container } = renderClaudeToolUse('Bash', { command: 'echo short but visually wrapped' })
      await new Promise(resolve => setTimeout(resolve, 0))

      expect(container.querySelector('[aria-label="Show full command"]')).not.toBeNull()
    }
    finally {
      scrollHeight.mockRestore()
      clientHeight.mockRestore()
    }
  })

  it('clears stale Claude Bash overflow when the command changes while expanded', async () => {
    const dims = mockCollapsedCommandOverflow()
    try {
      const [command, setCommand] = createSignal('echo short but visually wrapped')
      const { container } = render(() => (
        <ToolUseMessage
          toolName="Bash"
          icon={Terminal}
          title="Run command"
          fullCommand={command()}
          fallbackDisplay={null}
        />
      ))
      await settleCommandOverflowMeasure()
      expect(showFullCommandButton(container)).not.toBeNull()

      fireEvent.click(showFullCommandButton(container)!)
      dims.fit()
      setCommand('pwd')
      await settleCommandOverflowMeasure()

      expect(showFullCommandButton(container)).toBeNull()
      expect(container.textContent ?? '').toContain('pwd')
    }
    finally {
      dims.restore()
    }
  })

  it('clears stale Pi Bash overflow when the command changes while expanded', async () => {
    const dims = mockCollapsedCommandOverflow()
    try {
      const [command, setCommand] = createSignal('echo short but visually wrapped')
      const { container } = render(() => (
        <PiBashRenderer
          payload={{
            type: 'tool_execution_start',
            toolCallId: 'call_bash_1',
            toolName: 'bash',
            args: { command: command() },
          }}
        />
      ))
      await settleCommandOverflowMeasure()
      expect(showFullCommandButton(container)).not.toBeNull()

      fireEvent.click(showFullCommandButton(container)!)
      dims.fit()
      setCommand('pwd')
      await settleCommandOverflowMeasure()

      expect(showFullCommandButton(container)).toBeNull()
      expect(container.textContent ?? '').toContain('pwd')
    }
    finally {
      dims.restore()
    }
  })

  it('clears stale ACP execute overflow when the command changes while expanded', async () => {
    const dims = mockCollapsedCommandOverflow()
    try {
      const [command, setCommand] = createSignal('echo short but visually wrapped')
      const { container } = render(() => (
        <ToolCallUpdateMessage
          toolUse={{
            sessionUpdate: 'tool_call_update',
            toolCallId: 'call_1',
            kind: 'execute',
            status: 'completed',
            title: 'Run command',
            rawInput: { command: command() },
            rawOutput: { metadata: { exit: 0 } },
            content: [],
          }}
        />
      ))
      await settleCommandOverflowMeasure()
      expect(showFullCommandButton(container)).not.toBeNull()

      fireEvent.click(showFullCommandButton(container)!)
      dims.fit()
      setCommand('pwd')
      await settleCommandOverflowMeasure()

      expect(showFullCommandButton(container)).toBeNull()
      expect(container.textContent ?? '').toContain('pwd')
    }
    finally {
      dims.restore()
    }
  })

  it('tokenizes command input asynchronously when syntax highlighting is allowed', async () => {
    tokenizeAsyncCalls.mockClear()

    const { container } = render(() => (
      <BashHighlightHtml
        class="summary"
        code="echo highlighted"
        context={{}}
        namespace="test.highlightedCommandSummary"
      />
    ))

    await Promise.resolve()

    expect(container.textContent).toContain('echo highlighted')
    expect(tokenizeAsyncCalls).toHaveBeenCalledWith('bash', 'echo highlighted')
    const token = container.querySelector('span[data-shiki-token]') as HTMLElement | null
    expect(token).not.toBeNull()
    expect(token?.style.getPropertyValue('--shiki-light')).toBeTruthy()
  })

  it('does not rewrite highlighted command DOM when syntax highlighting is paused', async () => {
    const [paused, setPaused] = createSignal(false)
    const cache = createMessageRenderCacheStore().forRow('command-summary')
    const { container } = render(() => (
      <BashHighlightHtml
        class="summary"
        code="echo hello"
        context={{
          renderCache: cache,
          syntaxHighlightingPaused: paused,
        }}
        namespace="test.commandSummary"
      />
    ))
    const summary = container.querySelector('.summary')!
    const records: MutationRecord[] = []
    const observer = new MutationObserver(mutations => records.push(...mutations))
    observer.observe(summary, { childList: true, subtree: true, characterData: true })

    setPaused(true)
    await Promise.resolve()
    observer.disconnect()

    expect(records).toHaveLength(0)
    expect(summary.textContent).toContain('echo hello')
  })

  it('keeps highlighted ANSI output when text selection becomes active', async () => {
    renderAnsiCalls.mockClear()
    const [selectionActive, setSelectionActive] = createSignal(false)
    const cache = createMessageRenderCacheStore().forRow('ansi-selection')
    const { container } = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={'\x1B[31mred\x1B[0m'}
        isCollapsed={false}
        context={{
          renderCache: cache,
          textSelectionActive: selectionActive,
        }}
      />
    ))

    expect(container.querySelector('pre.shiki')).not.toBeNull()
    renderAnsiCalls.mockClear()

    setSelectionActive(true)
    await Promise.resolve()

    expect(container.querySelector('pre.shiki')).not.toBeNull()
    expect(renderAnsiCalls).not.toHaveBeenCalled()
  })
})

describe('command result scroll-critical rendering', () => {
  it('keeps terminal Codex command output mounted while syntax highlighting is paused', () => {
    normalizeProgressOutputCalls.mockClear()
    normalizedCommandBodyCalls.mockClear()
    stripToolUseHeaderFromOutputCalls.mockClear()

    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'printf expensive-output',
      aggregatedOutput: 'expensive-output\nline 2\nline 3\nline 4',
      exitCode: 0,
      status: 'completed',
    }, {
      syntaxHighlightingPaused: () => true,
    })

    const text = container.textContent ?? ''
    expect(container.querySelector('[data-command-result-deferred]')).toBeNull()
    expect(text).toContain('expensive-output')
    expect(text).not.toContain('Command output deferred while scrolling')
    expect(normalizedCommandBodyCalls).toHaveBeenCalled()
    expect(stripToolUseHeaderFromOutputCalls).toHaveBeenCalled()
  })

  it('keeps terminal Codex command output mounted while text selection is active', () => {
    normalizedCommandBodyCalls.mockClear()
    stripToolUseHeaderFromOutputCalls.mockClear()

    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'printf selected-output',
      aggregatedOutput: 'selected-output\nline 2\nline 3\nline 4',
      exitCode: 0,
      status: 'completed',
    }, {
      syntaxHighlightingPaused: () => true,
      textSelectionActive: () => true,
    })

    const text = container.textContent ?? ''
    expect(text).toContain('selected-output')
    expect(text).not.toContain('Command output deferred while scrolling')
    expect(normalizedCommandBodyCalls).toHaveBeenCalled()
    expect(stripToolUseHeaderFromOutputCalls).toHaveBeenCalled()
  })

  it('keeps terminal Codex command toolbar metadata while scroll-critical', () => {
    normalizeProgressOutputCalls.mockClear()
    normalizedCommandBodyCalls.mockClear()
    stripToolUseHeaderFromOutputCalls.mockClear()

    const { container } = renderCodexMessageBubble({
      type: 'commandExecution',
      command: 'printf metadata-output',
      aggregatedOutput: 'metadata-output\nline 2\nline 3\nline 4',
      exitCode: 0,
      status: 'completed',
    }, {
      syntaxHighlightingPaused: () => true,
    })

    expect(container.querySelector('[data-command-result-deferred]')).toBeNull()
    expect(container.textContent ?? '').toContain('metadata-output')
    expect(container.querySelector('[aria-label="Expand"]')).not.toBeNull()
    expect(normalizeProgressOutputCalls).toHaveBeenCalled()
    expect(normalizedCommandBodyCalls).toHaveBeenCalled()
    expect(stripToolUseHeaderFromOutputCalls).toHaveBeenCalled()
  })

  it('renders terminal Codex ANSI command output with ANSI highlighting when unpaused', () => {
    renderAnsiCalls.mockClear()

    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'printf colored-output',
      aggregatedOutput: '\x1B[31mcolored-output\x1B[0m',
      exitCode: 0,
      status: 'completed',
    })

    expect(container.textContent ?? '').toContain('colored-output')
    expect(renderAnsiCalls).toHaveBeenCalled()
    expect(container.querySelector('pre.shiki')).not.toBeNull()
  })

  it('keeps terminal Codex command output mounted during premeasure without ANSI rendering', () => {
    renderAnsiCalls.mockClear()
    normalizedCommandBodyCalls.mockClear()
    stripToolUseHeaderFromOutputCalls.mockClear()

    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'printf premeasure-output',
      aggregatedOutput: '\x1B[31mpremeasure-output\x1B[0m\nline 2',
      exitCode: 0,
      status: 'completed',
    }, {
      premeasureMode: true,
    })

    const text = container.textContent ?? ''
    expect(text).toContain('premeasure-output')
    expect(renderAnsiCalls).not.toHaveBeenCalled()
    expect(normalizedCommandBodyCalls).toHaveBeenCalled()
    expect(stripToolUseHeaderFromOutputCalls).toHaveBeenCalled()
  })

  it('does not auto-expand live Codex command UI state during premeasure', async () => {
    const setMessageUiState = vi.fn()
    renderCodexMessageBubble({
      type: 'commandExecution',
      command: 'printf live-output',
      status: 'running',
    }, {
      commandStream: () => [{ kind: 'output', text: 'live-output' }],
      getMessageUiState: () => false,
      setMessageUiState,
    }, { premeasureMode: true })

    await Promise.resolve()

    expect(setMessageUiState).not.toHaveBeenCalled()
  })

  it('still auto-expands live Codex command UI state during visible rendering', async () => {
    const setMessageUiState = vi.fn()
    renderCodexMessageBubble({
      type: 'commandExecution',
      command: 'printf live-output',
      status: 'running',
    }, {
      commandStream: () => [{ kind: 'output', text: 'live-output' }],
      getMessageUiState: () => false,
      setMessageUiState,
    })

    await Promise.resolve()

    expect(setMessageUiState).toHaveBeenCalledWith(MESSAGE_UI_KEY.CODEX_COMMAND_EXECUTION, true)
  })
})

describe('claude Bash interrupted renders "Interrupted"', () => {
  it('renders the Interrupted status when tool_use_result.interrupted is true', () => {
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: 'partial',
      interrupted: true,
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('Interrupted')
  })
})

describe('claude Bash success hides the success status row', () => {
  it('renders stdout without a Success label', () => {
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: 'all good',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('all good')
    expect(container.textContent ?? '').not.toContain('Success')
  })
})

describe('command result with empty output shows a hint with duration/exit', () => {
  it('codex commandExecution: empty aggregatedOutput renders [no output] · duration · exit', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'bun eslint src',
      aggregatedOutput: null,
      exitCode: 0,
      durationMs: 994,
      status: 'completed',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('[no output]')
    expect(text).toContain('994ms')
    expect(text).toContain('exit 0')
  })

  it('codex commandExecution: empty output without duration omits the duration part', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'true',
      aggregatedOutput: '',
      exitCode: 0,
      status: 'completed',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('[no output]')
    expect(text).toContain('exit 0')
    expect(text).not.toContain('ms')
  })

  it('codex commandExecution: non-empty output does not render [no output]', () => {
    const { container } = renderCodexItem({
      type: 'commandExecution',
      command: 'echo hi',
      aggregatedOutput: 'hi',
      exitCode: 0,
      durationMs: 12,
      status: 'completed',
    })
    const text = container.textContent ?? ''
    expect(text).not.toContain('[no output]')
    expect(text).toContain('hi')
  })

  it('claude Bash: empty stdout renders [no output] hint', () => {
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: '',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    expect(container.textContent ?? '').toContain('[no output]')
  })
})

describe('command result \\r progress normalization', () => {
  it('claude Bash: 4 \\r-separated progress segments render verbatim with no ellipsis', () => {
    // Default 3-row collapse would hide the tail; the threshold widening for
    // hadCarriageReturns must keep all 4 lines visible.
    const parsed = makeBashResult({
      tool_name: 'Bash',
      stdout: 'Rebasing (1/4)\rRebasing (2/4)\rRebasing (3/4)\rDone',
    }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    const text = container.textContent ?? ''
    expect(text).toContain('Rebasing (1/4)')
    expect(text).toContain('Rebasing (2/4)')
    expect(text).toContain('Rebasing (3/4)')
    expect(text).toContain('Done')
    expect(text).not.toContain('…')
  })

  it('claude Bash: 8 \\r-separated progress segments collapse to head 3 + … + tail 3', () => {
    const stdout = ['s1', 's2', 's3', 's4', 's5', 's6', 's7', 's8'].join('\r')
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, '')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Bash' })
    const text = container.textContent ?? ''
    expect(text).toContain('s1')
    expect(text).toContain('s2')
    expect(text).toContain('s3')
    expect(text).toContain('…')
    expect(text).toContain('s6')
    expect(text).toContain('s7')
    expect(text).toContain('s8')
    expect(text).not.toContain('s4')
    expect(text).not.toContain('s5')
  })
})

// Regression for "no expand/collapse button on rebase output". The body
// normalizes \r-overwrites into separate lines (so a 3-raw-line stdout can
// render as 9 lines), but `meta.collapsible` was counting raw \n only — so
// the toolbar's expand button never appeared over output the body actually
// clipped. These tests pin the post-normalize line count into the meta so
// the toolbar and body agree.
describe('command result collapsibility accounts for \\r-normalized line count', () => {
  it('claude Bash: rebase progress (3 raw lines, 9 normalized lines) is reported as collapsible', () => {
    const stdout = 'From github.com:leapmux/leapmux\n * branch              main       -> FETCH_HEAD\nRebasing (1/6)\rRebasing (2/6)\rRebasing (3/6)\rRebasing (4/6)\rRebasing (5/6)\rRebasing (6/6)\rSuccessfully rebased and updated refs/heads/grid-layout.'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(true)
  })

  it('claude Bash: short \\r progress (no clip) is NOT reported as collapsible', () => {
    // 4 \r-segments → 4 normalized lines. Threshold widens to 7 because of
    // the \r, so 4 ≤ 7 means the body shows everything; meta should agree.
    const stdout = 'Rebasing (1/4)\rRebasing (2/4)\rRebasing (3/4)\rDone'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(false)
  })

  it('claude Bash: plain output preserves the standard 3-row collapse threshold', () => {
    const stdout = 'a\nb\nc\nd'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(true)
  })

  it('claude Bash: plain output at the threshold (3 lines, no \\r) is NOT collapsible', () => {
    const stdout = 'a\nb\nc'
    const parsed = makeBashResult({ tool_name: 'Bash', stdout }, stdout)
    const meta = claudeToolResultMeta({ kind: 'tool_result' }, parsed, 'Bash', undefined)
    expect(meta?.collapsible).toBe(false)
  })

  it('codex commandExecution: rebase-style \\r progress is reported as collapsible', () => {
    const aggregatedOutput = 'From origin\n * branch    main       -> FETCH_HEAD\nRebasing (1/6)\rRebasing (2/6)\rRebasing (3/6)\rRebasing (4/6)\rRebasing (5/6)\rRebasing (6/6)\rDone.'
    const meta = codexToolResultMeta(
      { kind: 'tool_use', toolName: 'commandExecution', toolUse: {}, content: [] },
      { item: { type: 'commandExecution', status: 'completed', aggregatedOutput, exitCode: 0 } },
      'commandExecution',
      undefined,
    )
    expect(meta?.collapsible).toBe(true)
  })
})
