import type { AskQuestionState } from '../../controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { ZCODE_MODE, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions, ControlRequestContent } from '../../ControlRequestBanner'
import { ZCodeControlActions, ZCodeControlContent } from './controls'
import { ZCODE_METHOD } from './protocol'
import './plugin'

function makeAskState(selections: Record<number, string[]> = {}): AskQuestionState {
  const [sel, setSelections] = createSignal<Record<number, string[]>>(selections)
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>({})
  const [currentPage, setCurrentPage] = createSignal(0)
  return { selections: sel, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

/**
 * A stored control request. `tool_name` is how the worker records WHICH of the three
 * prompts arrived, and it is what the dispatchers switch on.
 */
function request(
  toolName: string,
  input: Record<string, unknown> = {},
  params: Record<string, unknown> = {},
): ControlRequest {
  return {
    requestId: 'req-1',
    agentId: 'agent-1',
    payload: {
      method: toolName === ZCODE_TOOL.Bash ? ZCODE_METHOD.RequestPermission : ZCODE_METHOD.RequestUserInput,
      request: { tool_name: toolName, input },
      params,
    },
  }
}

function decode(bytes: unknown): Record<string, unknown> {
  return JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
}

describe('zcode plan approval control', () => {
  // The shared ExitPlanModeContent cannot be reused: it renders Claude's
  // allowedPrompts summary, which ZCode does not send, and it would show "ready to
  // proceed" while dropping the plan itself.
  it('renders the plan the request carries as its question text', () => {
    const { container } = render(() => (
      <ZCodeControlContent
        request={request(ZCODE_TOOL.ExitPlanMode, { questions: [{ question: '# Plan\n\nStep one' }] })}
        askState={makeAskState()}
      />
    ))
    expect(container.textContent ?? '').toContain('Plan Ready for Review')
    expect(container.textContent ?? '').toContain('Step one')
  })

  it('falls back to a sentence when the request states no plan', () => {
    const { container } = render(() => (
      <ZCodeControlContent request={request(ZCODE_TOOL.ExitPlanMode)} askState={makeAskState()} />
    ))
    expect(container.textContent ?? '').toContain('ready to proceed')
  })

  // Every case sends the shared allow/deny envelope unchanged. The worker translates
  // it into the app-server's accept/decline reply when it forwards it.
  it('approves through the shared plan actions with the neutral allow envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <ZCodeControlActions
        request={request(ZCODE_TOOL.ExitPlanMode, { questions: [{ question: 'the plan' }] })}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    fireEvent.click(getByTestId('plan-approve-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(decode(onRespond.mock.calls[0][1])).toMatchObject({
      type: 'control_response',
      response: { request_id: 'req-1', response: { behavior: 'allow' } },
    })
  })

  // Reject routes through the composer so the user can type the reason first; the
  // envelope is sent by the send path, not by the button.
  it('rejects by handing the turn to the composer send path', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const onTriggerSend = vi.fn()
    const { getByTestId } = render(() => (
      <ZCodeControlActions
        request={request(ZCODE_TOOL.ExitPlanMode, { questions: [{ question: 'the plan' }] })}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={onTriggerSend}
      />
    ))
    fireEvent.click(getByTestId('plan-reject-btn'))
    expect(onTriggerSend).toHaveBeenCalledOnce()
    expect(onRespond).not.toHaveBeenCalled()
  })
})

describe('zcode question control', () => {
  const questionInput = {
    questions: [{
      question: 'Which database?',
      options: [{ value: 'Postgres' }, { value: 'MySQL' }],
    }],
  }

  it('renders the question and its options through the shared control', () => {
    const { container, getByTestId } = render(() => (
      <ControlRequestContent
        request={request(ZCODE_TOOL.AskUserQuestion, questionInput)}
        askState={makeAskState()}
        agentProvider={AgentProvider.ZCODE}
      />
    ))
    expect(container.textContent ?? '').toContain('Which database?')
    expect(getByTestId('question-option-Postgres')).toBeInTheDocument()
    expect(getByTestId('question-option-MySQL')).toBeInTheDocument()
  })

  it('ships the picked option in the shared allow envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <ControlRequestActions
        request={request(ZCODE_TOOL.AskUserQuestion, questionInput)}
        askState={makeAskState({ 0: ['MySQL'] })}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
        agentProvider={AgentProvider.ZCODE}
      />
    ))
    fireEvent.click(getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(decode(onRespond.mock.calls[0][1])).toMatchObject({
      response: {
        response: {
          behavior: 'allow',
          updatedInput: { answers: { 'Which database?': 'MySQL' } },
        },
      },
    })
  })
})

describe('zcode permission control', () => {
  // ZCode's `reason` is its own explanation of why the call needs approval, and it is
  // the most useful line in the banner.
  it('shows the reason above the shared tool input', () => {
    const { container } = render(() => (
      <ZCodeControlContent
        request={request(ZCODE_TOOL.Bash, { command: 'rm -rf build' }, { reason: 'deletes files' })}
        askState={makeAskState()}
      />
    ))
    expect(container.textContent ?? '').toContain('deletes files')
    expect(container.textContent ?? '').toContain('rm -rf build')
  })

  it('renders without a reason when the request states none', () => {
    const { container } = render(() => (
      <ZCodeControlContent
        request={request(ZCODE_TOOL.Bash, { command: 'ls' })}
        askState={makeAskState()}
      />
    ))
    expect(container.textContent ?? '').toContain('ls')
  })

  it('allows with the neutral allow envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByTestId } = render(() => (
      <ZCodeControlActions
        request={request(ZCODE_TOOL.Bash, { command: 'ls' })}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(decode(onRespond.mock.calls[0][1])).toMatchObject({
      response: { response: { behavior: 'allow' } },
    })
  })

  it('rejects immediately when the editor is empty', () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const onTriggerSend = vi.fn()
    const { getByTestId } = render(() => (
      <ZCodeControlActions
        request={request(ZCODE_TOOL.Bash, { command: 'ls' })}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={onTriggerSend}
      />
    ))
    fireEvent.click(getByTestId('control-deny-btn'))
    expect(onTriggerSend).not.toHaveBeenCalled()
    expect(decode(onRespond.mock.calls[0][1])).toMatchObject({
      response: { response: { behavior: 'deny' } },
    })
  })

  // ZCode declares `yolo` as its bypass mode, so the banner offers a bypass switch.
  // It must allow FIRST and switch the mode after: applying a mode the
  // provider cannot take live relaunches the agent, and a relaunch that won the race
  // would kill the session before the allow arrived.
  it('allows and then switches to the bypass mode', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const order: string[] = []
    onRespond.mockImplementation(async () => {
      order.push('allow')
    })
    const { getByTestId } = render(() => (
      <ZCodeControlActions
        request={request(ZCODE_TOOL.Bash, { command: 'ls' })}
        askState={makeAskState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
        bypass={{
          settings: { sets: { permissionMode: ZCODE_MODE.Yolo } },
          apply: (change) => {
            order.push(`mode:${change.sets.permissionMode}`)
          },
        }}
      />
    ))
    fireEvent.click(getByTestId('control-bypass-permissions-checkbox').querySelector('input')!)
    fireEvent.click(getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(order).toEqual(['allow', `mode:${ZCODE_MODE.Yolo}`]))
  })

  // A tool name the dispatcher does not know is a permission, which is the fallback
  // case. A tool that ZCode adds later still gets an actionable banner.
  it('treats an unknown tool name as a permission', () => {
    const { container } = render(() => (
      <ZCodeControlContent
        request={request('SomeToolAddedLater', { arg: 1 }, { reason: 'unfamiliar' })}
        askState={makeAskState()}
      />
    ))
    expect(container.textContent ?? '').toContain('unfamiliar')
  })
})
