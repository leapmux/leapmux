import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '~/components/chat/controls/types'
import { KIMI_DISPLAY, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions, ControlRequestContent } from '~/test-support/controlRequestBanner'
import { kimiApprovalRequest, kimiQuestionRequest } from '~/test-support/kimiFixtures'
import '../index'

function request(payload: Record<string, unknown>, requestId = 'approval_1'): ControlRequest {
  return { requestId, agentId: 'agent-1', payload }
}

function renderActions(req: ControlRequest, onRespond = vi.fn().mockResolvedValue(undefined)) {
  const answerState = createControlAnswerState()
  render(() => (
    <ControlRequestActions
      request={req}
      answerState={answerState}
      agentProvider={AgentProvider.KIMI_CODE}
      onRespond={onRespond}
      hasEditorContent={false}
      onTriggerSend={() => {}}
    />
  ))
  return { onRespond, answerState }
}

function sentBody(onRespond: ReturnType<typeof vi.fn>, call = 0) {
  const [bytes] = onRespond.mock.calls[call] ?? []
  return JSON.parse(new TextDecoder().decode(bytes)).response.response
}

describe('kimi control channel', () => {
  it('answers a command approval through the permission options', async () => {
    const { onRespond } = renderActions(request(kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' })))
    fireEvent.click(screen.getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toStrictEqual({ behavior: 'allow' })
  })

  it('denies a command approval', async () => {
    const { onRespond } = renderActions(request(kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' })))
    fireEvent.click(screen.getByTestId('control-deny-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond).behavior).toBe('deny')
  })

  it('offers the plan choices beside Approve and Reject', async () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, {
      kind: KIMI_DISPLAY.PlanReview,
      plan: '# Plan',
      options: [{ label: 'Option A' }],
    })
    const { onRespond } = renderActions(request(payload))
    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('control-more-actions'))
    expect(screen.getByTestId('plan-choice-0')).toHaveTextContent('Approve: Option A')
    fireEvent.click(screen.getByTestId('plan-choice-2'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toMatchObject({ behavior: 'deny', choice: 'Reject and Exit' })
  })

  it('draws the plan a plan review carries', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, { kind: KIMI_DISPLAY.PlanReview, plan: '# Refactor the parser' })
    render(() => (
      <ControlRequestContent request={request(payload)} answerState={createControlAnswerState()} agentProvider={AgentProvider.KIMI_CODE} />
    ))
    expect(screen.getByTestId('control-banner')).toHaveTextContent('Refactor the parser')
  })

  it('answers a subagent plan review through the permission options', async () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, {
      kind: KIMI_DISPLAY.PlanReview,
      plan: '# Split the parser',
      options: [{ label: 'Option A' }],
    }, { agent_id: 'agent-0', agentId: 'agent-0' })
    const { onRespond } = renderActions(request(payload))
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('control-more-actions'))
    fireEvent.click(screen.getByTestId('control-decision-plan_reject:Revise'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toMatchObject({ behavior: 'deny', choice: 'Revise' })
  })

  it('draws the plan a subagent plan review carries', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, { kind: KIMI_DISPLAY.PlanReview, plan: '# Split the parser' }, { agent_id: 'agent-0', agentId: 'agent-0' })
    render(() => (
      <ControlRequestContent request={request(payload)} answerState={createControlAnswerState()} agentProvider={AgentProvider.KIMI_CODE} />
    ))
    expect(screen.getByTestId('control-banner')).toHaveTextContent('Permission Required')
    expect(screen.getByTestId('control-banner')).toHaveTextContent('Split the parser')
  })

  it('answers a command approval for the session through the allow scope', async () => {
    const { onRespond } = renderActions(request(kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' })))
    fireEvent.click(screen.getByRole('radio', { name: 'Session' }))
    fireEvent.click(screen.getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toStrictEqual({ behavior: 'allow', scope: 'session' })
  })

  it('starts a goal in the permission mode the reader picks', async () => {
    const { onRespond } = renderActions(request(kimiApprovalRequest(KIMI_TOOL.CreateGoal, { kind: KIMI_DISPLAY.GoalStart, objective: 'Ship it' })))
    fireEvent.click(screen.getByTestId('control-more-actions'))
    fireEvent.click(screen.getByTestId('control-decision-goal_mode:yolo'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toStrictEqual({ behavior: 'allow', choice: 'yolo' })
  })

  it('submits the selected option ids as the answers of a question request', async () => {
    const payload = kimiQuestionRequest([{ id: 'q_0', question: 'Which color?', options: [{ id: 'opt_0_0', label: 'Red' }, { id: 'opt_0_1', label: 'Blue' }] }])
    const answerState = createControlAnswerState({ selections: { 0: ['opt_0_1'] } })
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <ControlRequestActions request={request(payload, 'question_1')} answerState={answerState} agentProvider={AgentProvider.KIMI_CODE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
    ))
    fireEvent.click(screen.getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toStrictEqual({ behavior: 'allow', answers: { q_0: { kind: 'single', option_id: 'opt_0_1' } } })
  })

  it('stops a question request as a refusal that carries the stop reason', async () => {
    const payload = kimiQuestionRequest([{ id: 'q_0', question: 'Which color?', options: [{ id: 'opt_0_0', label: 'Red' }] }])
    const onRespond = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <ControlRequestActions request={request(payload, 'question_1')} answerState={createControlAnswerState()} agentProvider={AgentProvider.KIMI_CODE} onRespond={onRespond} hasEditorContent={false} onTriggerSend={() => {}} />
    ))
    fireEvent.click(screen.getByTestId('control-stop-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    expect(sentBody(onRespond)).toStrictEqual({ behavior: 'deny', message: 'User stopped' })
  })

  it('draws the questions of a question request', () => {
    const payload = kimiQuestionRequest([{ id: 'q_0', question: 'Which color?', options: [{ id: 'opt_0_0', label: 'Red' }, { id: 'opt_0_1', label: 'Blue' }] }])
    render(() => (
      <ControlRequestContent request={request(payload, 'question_1')} answerState={createControlAnswerState()} agentProvider={AgentProvider.KIMI_CODE} />
    ))
    expect(screen.getByTestId('control-banner')).toHaveTextContent('Which color?')
    expect(screen.getByTestId('control-banner')).toHaveTextContent('Blue')
  })
})
