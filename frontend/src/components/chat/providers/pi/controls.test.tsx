import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { ControlRequestActions, ControlRequestContent } from '../../ControlRequestBanner'
import { createControlAnswerState } from '../../controls/types'
import { PiControlActions, PiControlContent } from './controls'
import './plugin'

const makeAskState = createControlAnswerState

function makeSelectRequest(): ControlRequest {
  return {
    requestId: 'req-1',
    agentId: 'agent-1',
    payload: {
      type: 'extension_ui_request',
      id: 'req-1',
      method: 'select',
      title: 'Allow dangerous command?',
      options: ['Allow', 'Block'],
    },
  }
}

describe('pi select control requests', () => {
  it('renders complete MCP permission arguments through the shared source reader', async () => {
    const args = { query: 'x'.repeat(900), limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const preview = `${JSON.stringify(args, null, 2).replace(/\s+/g, ' ').slice(0, 500)}...`
    const request = { requestId: 'mcp-permission', agentId: 'agent-1', sourceSeq: 7n, payload: {
      type: 'extension_ui_request',
      id: 'mcp-permission',
      method: 'select',
      title: `MCP: probe wants to run write\n\nArguments:\n${preview}`,
      options: ['Allow once', 'Allow for session', 'Deny'],
    } }
    const source = makeMessage({ seq: 7n, agentProvider: AgentProvider.PI, spanId: 'mcp-tool', content: rawContent({ type: 'tool_execution_start', toolCallId: 'mcp-tool', toolName: 'mcp', args: { tool: 'probe_write', args } }) })
    const messageContext = testMessageContext({ fetchMessage: async () => source })
    const original = JSON.stringify(request.payload)
    const { findByText, container } = render(() => <ControlRequestContent request={request} messageContext={messageContext} answerState={makeAskState()} agentProvider={AgentProvider.PI} />)
    expect(await findByText('Permission Required')).toBeVisible()
    await vi.waitFor(() => expect(container.querySelector('pre')?.textContent).toContain('END_MCP_ARGUMENTS'))
    expect(JSON.stringify(request.payload)).toBe(original)
  })

  it.each([false, true])('uses shared plan approval controls (clearContext=%s)', async (clearContext) => {
    const request = {
      requestId: 'plan-dialog',
      agentId: 'agent-1',
      payload: {
        type: 'extension_ui_request',
        id: 'plan-dialog',
        method: 'select',
        title: 'Proposed plan ready. What next?\nPlan details belong in the transcript.',
        options: ['Implement here', 'Start fresh and implement', 'Implementation options…', 'Export plan…', 'Save for later', 'Stay in Plan mode', 'Discard plan and exit'],
      },
    }
    const answerState = makeAskState()
    const onRespond = vi.fn(async (_content: Uint8Array) => {})
    const { getByText, getByTestId, queryByRole } = render(() => (
      <>
        <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.PI} />
        <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.PI} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    expect(getByText('Plan Ready for Review')).toBeVisible()
    expect(queryByRole('radio')).toBeNull()
    if (clearContext)
      fireEvent.click(getByTestId('plan-clear-context-checkbox'))
    fireEvent.click(getByTestId('plan-approve-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0])))
      .toEqual({ type: 'extension_ui_response', id: 'plan-dialog', value: clearContext ? 'Start fresh and implement' : 'Implement here' })
  })

  it('allows an explicit empty answer to a native input dialog', async () => {
    const request = { requestId: 'empty', agentId: 'agent-1', payload: { type: 'extension_ui_request', id: 'empty', method: 'input', title: 'Choose any options', placeholder: '1,3' } }
    const onRespond = vi.fn(async (_content: Uint8Array) => {})
    const { getByTestId } = render(() => <ControlRequestActions request={request} answerState={makeAskState()} agentProvider={AgentProvider.PI} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />)
    const submit = getByTestId('control-submit-btn')
    expect(submit).toBeEnabled()
    fireEvent.click(submit)
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toEqual({ type: 'extension_ui_response', id: 'empty', value: '' })
  })

  it('loads complete previews from the stored source outside the visible transcript', async () => {
    const preview = `\`\`\`txt\n${'preview line\n'.repeat(80)}complete-preview-end\n\`\`\``
    const questions = [{ question: 'Choose a layout', header: 'Layout', options: [{ label: 'Compact', description: 'Small', preview }, { label: 'Wide', description: 'Large' }] }]
    const source = makeMessage({ seq: 7n, agentProvider: AgentProvider.PI, spanId: 'question-tool', content: rawContent({ type: 'tool_execution_start', toolCallId: 'question-tool', toolName: 'ask_user_question', args: { questions } }) })
    const fetchMessage = vi.fn(async () => source)
    const messageContext = testMessageContext({ fetchMessage })
    const request = {
      requestId: 'dialog',
      agentId: 'agent-1',
      sourceSeq: 7n,
      payload: { type: 'extension_ui_request', id: 'dialog', method: 'select', title: `[Layout] Choose a layout\n\n--- 1. Compact preview ---\n${preview.slice(0, 600)}`, options: ['1. Compact — Small', '2. Wide — Large', '3. Type something.'] },
    }
    const original = JSON.stringify(request.payload)
    const { findByRole } = render(() => <ControlRequestContent request={request} messageContext={messageContext} answerState={makeAskState()} agentProvider={AgentProvider.PI} />)
    expect(await findByRole('region', { name: 'Compact preview' })).toHaveTextContent('complete-preview-end')
    expect(fetchMessage).toHaveBeenCalledOnce()
    expect(fetchMessage).toHaveBeenCalledWith(7n, expect.any(AbortSignal))
    expect(JSON.stringify(request.payload)).toBe(original)
  })

  it('sends source-backed multi-select choices as the native comma-separated numbers', async () => {
    const questions = [{ question: 'Choose', multiSelect: true, options: [{ label: 'A', description: 'first', preview: 'A preview' }, { label: 'B', description: 'second', preview: 'B preview' }] }]
    const source = makeMessage({ seq: 7n, agentProvider: AgentProvider.PI, spanId: 'question-tool', content: rawContent({ type: 'tool_execution_start', toolCallId: 'question-tool', toolName: 'ask_user_question', args: { questions } }) })
    const messageContext = testMessageContext({ messages: () => [source] })
    const request = {
      requestId: 'dialog',
      agentId: 'agent-1',
      sourceSeq: 7n,
      payload: { type: 'extension_ui_request', id: 'dialog', method: 'input', title: 'Choose\n\n1. A — first\n2. B — second\n\nChoose all that apply', placeholder: '1,3' },
    }
    const answerState = makeAskState()
    const onRespond = vi.fn(async (_content: Uint8Array) => {})
    const { getByTestId, getAllByRole } = render(() => (
      <>
        <ControlRequestContent request={request} messageContext={messageContext} answerState={answerState} agentProvider={AgentProvider.PI} />
        <ControlRequestActions request={request} messageContext={messageContext} answerState={answerState} agentProvider={AgentProvider.PI} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
      </>
    ))
    const options = getAllByRole('checkbox')
    fireEvent.click(options[0])
    fireEvent.click(options[1])
    fireEvent.click(getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0][0]))).toEqual({ type: 'extension_ui_response', id: 'dialog', value: '1,2' })
  })

  it('renders select options through the shared AskUserQuestion content', () => {
    const { container, getByTestId } = render(() => (
      <ControlRequestContent request={makeSelectRequest()} answerState={makeAskState()} agentProvider={AgentProvider.PI} />
    ))

    expect(container.textContent ?? '').toContain('Agent Question')
    expect(container.textContent ?? '').toContain('Allow dangerous command?')
    expect(getByTestId('question-option-Allow')).toBeInTheDocument()
    expect(getByTestId('question-option-Block')).toBeInTheDocument()
  })

  it('renders the default title when the payload omits one', () => {
    const req: ControlRequest = {
      requestId: 'req-2',
      agentId: 'agent-1',
      payload: {
        type: 'extension_ui_request',
        id: 'req-2',
        method: 'select',
        options: ['One', 'Two'],
      },
    }
    const { container } = render(() => (
      <ControlRequestContent request={req} answerState={makeAskState()} agentProvider={AgentProvider.PI} />
    ))
    expect(container.textContent ?? '').toContain('Choose an option')
  })

  it('drops non-string options defensively', () => {
    const req: ControlRequest = {
      requestId: 'req-3',
      agentId: 'agent-1',
      payload: {
        type: 'extension_ui_request',
        id: 'req-3',
        method: 'select',
        title: 'Pick one',
        options: ['Real', 42, null, 'Other'],
      },
    }
    const { queryByTestId } = render(() => (
      <ControlRequestContent request={req} answerState={makeAskState()} agentProvider={AgentProvider.PI} />
    ))
    expect(queryByTestId('question-option-Real')).toBeInTheDocument()
    expect(queryByTestId('question-option-Other')).toBeInTheDocument()
    expect(queryByTestId('question-option-42')).toBeNull()
  })

  it('submits the selected option as a Pi extension_ui_response value', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <ControlRequestActions
        request={makeSelectRequest()}
        answerState={makeAskState({ selections: { 0: ['Block'] } })}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
        agentProvider={AgentProvider.PI}
      />
    ))

    fireEvent.click(getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-1',
      value: 'Block',
    })
  })

  it('cancels via the Stop button as a Pi cancellation envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <ControlRequestActions
        request={makeSelectRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
        agentProvider={AgentProvider.PI}
      />
    ))

    fireEvent.click(getByTestId('control-stop-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())

    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-1',
      cancelled: true,
    })
  })
})

function makeConfirmRequest(): ControlRequest {
  return {
    requestId: 'req-c',
    agentId: 'agent-1',
    payload: {
      type: 'extension_ui_request',
      id: 'req-c',
      method: 'confirm',
      title: 'Continue?',
      message: 'About to delete files.',
    },
  }
}

function makeInputRequest(prefill = ''): ControlRequest {
  return {
    requestId: 'req-i',
    agentId: 'agent-1',
    payload: {
      type: 'extension_ui_request',
      id: 'req-i',
      method: 'input',
      title: 'Enter value',
      placeholder: 'type here',
      prefill,
    },
  }
}

function makeEditorRequest(prefill = ''): ControlRequest {
  return {
    requestId: 'req-e',
    agentId: 'agent-1',
    payload: {
      type: 'extension_ui_request',
      id: 'req-e',
      method: 'editor',
      title: 'Edit text',
      prefill,
    },
  }
}

describe('pi confirm control requests', () => {
  it('renders Approve and Deny buttons with the message body', () => {
    const { container } = render(() => (
      <PiControlActions
        request={makeConfirmRequest()}
        answerState={makeAskState()}
        onRespond={vi.fn()}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const { container: contentContainer } = render(() => (
      <PiControlContent request={makeConfirmRequest()} answerState={makeAskState()} />
    ))
    expect(contentContainer.textContent ?? '').toContain('Continue?')
    expect(contentContainer.textContent ?? '').toContain('About to delete files.')
    // The action surface produces the Approve / Deny buttons referenced by
    // the test name. Looked up through this render's own `container`, not
    // through `screen`, so the assertion cannot pick up the content render
    // above. A missing button then reads as `expected null to have class`.
    expect(container.querySelector('[data-testid="control-allow-btn"]')).toHaveClass(compactControl)
    expect(container.querySelector('[data-testid="control-deny-btn"]')).toHaveClass('outline', compactControl)
  })

  it('emits a confirm:true response on Approve', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeConfirmRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-c',
      confirmed: true,
    })
  })

  it('emits a confirm:false response on Deny', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeConfirmRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-c',
      confirmed: false,
    })
  })
})

describe('pi input control requests', () => {
  it('snapshots prefill into the editable input', () => {
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeInputRequest('initial')}
        answerState={makeAskState()}
        onRespond={vi.fn()}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const input = getByTestId('pi-input') as HTMLInputElement
    expect(input.value).toBe('initial')
  })

  it('send button ships the typed value', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeInputRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const input = getByTestId('pi-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'hello world' } })
    fireEvent.click(getByTestId('control-allow-btn'))

    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-i',
      value: 'hello world',
    })
  })

  it('enter in the input also ships the typed value', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeInputRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const input = getByTestId('pi-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'enterval' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({ value: 'enterval' })
  })

  it('cancel sends a Pi cancellation envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeInputRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-i',
      cancelled: true,
    })
  })
})

describe('pi editor control requests', () => {
  it('renders a multi-line textarea pre-populated with prefill', () => {
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeEditorRequest('first\nsecond')}
        answerState={makeAskState()}
        onRespond={vi.fn()}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const textarea = getByTestId('pi-editor') as HTMLTextAreaElement
    expect(textarea.value).toBe('first\nsecond')
  })

  it('send ships the textarea contents as a value response', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeEditorRequest()}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const textarea = getByTestId('pi-editor') as HTMLTextAreaElement
    fireEvent.input(textarea, { target: { value: 'multi\nline' } })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-e',
      value: 'multi\nline',
    })
  })
})

describe('pi unknown method fallback', () => {
  it('renders an Acknowledge button that emits confirm:true', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const req: ControlRequest = {
      requestId: 'req-u',
      agentId: 'agent-1',
      payload: {
        type: 'extension_ui_request',
        id: 'req-u',
        method: 'something_pi_added_later',
        title: 'Heads up',
      },
    }
    const { getByTestId } = render(() => (
      <PiControlActions
        request={req}
        answerState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    expect((getByTestId('control-allow-btn') as HTMLButtonElement).textContent).toContain('Acknowledge')
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      id: 'req-u',
      confirmed: true,
    })
  })
})
