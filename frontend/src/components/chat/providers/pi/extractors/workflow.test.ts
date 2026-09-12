import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { piWorkflowRequest, piWorkflowResult } from './workflow'

const script = 'export const meta = { name: "Probe", description: "Read the sample" };\nreturn "<strong>literal</strong>";'
const request = { type: 'tool_execution_start', toolCallId: 'workflow', toolName: 'SubagentWorkflow', args: { script, title: 'Ignored title', description: 'Ignored description' } }
const result = { type: 'tool_execution_end', toolCallId: 'workflow', toolName: 'SubagentWorkflow', result: { content: [{ type: 'text', text: 'Workflow "Probe" started in the background.\nTask ID: wf_probe\nScript: /project/probe.workflow.js\n\nWait for completion.' }], details: { taskId: 'wf_probe' } } }

describe('pi workflow sources', () => {
  it('uses the native workflow title and retains the exact script', () => {
    expect(piWorkflowRequest(request, undefined, input(result))).toMatchObject({ description: 'Probe', prompt: script, promptFormat: 'pre', promptLabel: 'Script' })
    expect(piWorkflowRequest(request).description).toBe('Run workflow')
  })

  it('does not evaluate the supplied script', () => {
    expect(piWorkflowRequest({ ...request, args: { script: 'throw new Error("Must not run")' } }).prompt).toBe('throw new Error("Must not run")')
  })

  it('keeps a successful launch running without repeating instructions', () => {
    expect(piWorkflowResult(result, input(request))).toMatchObject({ description: 'Probe', status: 'running', outcome: 'running', body: '', agentId: 'wf_probe' })
  })

  it('shows validation failures even when isError is false', () => {
    const failed = { ...result, isError: false, result: { content: [{ type: 'text', text: 'The script requires a meta block.' }] } }
    expect(piWorkflowResult(failed, input(request))).toMatchObject({ outcome: 'failed', body: 'The script requires a meta block.' })
  })

  it('preserves unexpected acknowledgement text and rejects an unrelated title', () => {
    const foreign = { ...result, result: { ...result.result, details: { taskId: 'wf_other' } } }
    expect(piWorkflowResult(foreign, input(request))).toMatchObject({ description: 'Run workflow', body: result.result.content[0].text })
    expect(piWorkflowRequest({ ...request, toolCallId: 'other' }, undefined, input(result)).description).toBe('Run workflow')
  })

  it('respects the script path precedence over ignored inline source', () => {
    const source = piWorkflowRequest({ ...request, args: { script, scriptPath: '/project/workflow.js', resumeFromRunId: 'wf_previous', args: [0, false] } })
    expect(source.prompt).toBe('')
    expect(source.metadata).toContainEqual({ label: 'Script', value: '/project/workflow.js' })
    expect(source.metadata).toContainEqual({ label: 'Previous run', value: 'wf_previous' })
  })
})
