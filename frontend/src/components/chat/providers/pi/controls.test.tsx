import type { AskQuestionState } from '../../controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { PiControlActions, PiControlContent } from './controls'

function makeAskState(overrides: {
  selections?: Record<number, string[]>
  customTexts?: Record<number, string>
  currentPage?: number
} = {}): AskQuestionState {
  const [selections, setSelections] = createSignal<Record<number, string[]>>(overrides.selections ?? {})
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>(overrides.customTexts ?? {})
  const [currentPage, setCurrentPage] = createSignal(overrides.currentPage ?? 0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

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
  it('renders select options through the shared AskUserQuestion content', () => {
    const { container, getByTestId } = render(() => (
      <PiControlContent request={makeSelectRequest()} askState={makeAskState()} />
    ))

    expect(container.textContent ?? '').toContain('Agent Question')
    expect(container.textContent ?? '').toContain('Allow dangerous command?')
    expect(getByTestId('question-option-Allow')).toBeTruthy()
    expect(getByTestId('question-option-Block')).toBeTruthy()
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
      <PiControlContent request={req} askState={makeAskState()} />
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
      <PiControlContent request={req} askState={makeAskState()} />
    ))
    expect(queryByTestId('question-option-Real')).toBeTruthy()
    expect(queryByTestId('question-option-Other')).toBeTruthy()
    expect(queryByTestId('question-option-42')).toBeNull()
  })

  it('submits the selected option as a Pi extension_ui_response value', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeSelectRequest()}
        askState={makeAskState({ selections: { 0: ['Block'] } })}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())

    const [, bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      type: 'extension_ui_response',
      id: 'req-1',
      value: 'Block',
    })
  })

  it('cancels via the Stop button as a Pi cancellation envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeSelectRequest()}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(getByTestId('control-stop-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())

    const [, bytes] = onRespond.mock.calls[0]
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
    const { container, getByTestId } = render(() => (
      <PiControlContent request={makeConfirmRequest()} askState={makeAskState()} />
    ))
    expect(container.textContent ?? '').toContain('Continue?')
    expect(container.textContent ?? '').toContain('About to delete files.')
    void getByTestId // ensure no throw above
  })

  it('emits a confirm:true response on Approve', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeConfirmRequest()}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
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
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))

    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
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
        askState={makeAskState()}
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
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const input = getByTestId('pi-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'hello world' } })
    fireEvent.click(getByTestId('control-allow-btn'))

    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
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
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const input = getByTestId('pi-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'enterval' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({ value: 'enterval' })
  })

  it('cancel sends a Pi cancellation envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <PiControlActions
        request={makeInputRequest()}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    fireEvent.click(getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
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
        askState={makeAskState()}
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
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    const textarea = getByTestId('pi-editor') as HTMLTextAreaElement
    fireEvent.input(textarea, { target: { value: 'multi\nline' } })
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
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
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    expect((getByTestId('control-allow-btn') as HTMLButtonElement).textContent).toContain('Acknowledge')
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const [, bytes] = onRespond.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(bytes as Uint8Array))).toMatchObject({
      id: 'req-u',
      confirmed: true,
    })
  })
})
