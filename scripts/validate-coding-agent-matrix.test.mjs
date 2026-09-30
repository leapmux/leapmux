import { mkdirSync, mkdtempSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { readCodingAgentMatrix, validateCodingAgentMatrix } from './validate-coding-agent-matrix.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const SCRATCH_ROOT = join(ROOT, '.tmp')
const directories = []

function fixture() {
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  const root = mkdtempSync(join(SCRATCH_ROOT, 'matrix-validator-'))
  directories.push(root)
  const spec = 'frontend/tests/e2e/claude-code/text-attachments.spec.ts'
  mkdirSync(join(root, 'icons', 'agents'), { recursive: true })
  mkdirSync(join(root, 'frontend', 'tests', 'e2e', 'claude-code'), { recursive: true })
  writeFileSync(join(root, 'icons', 'agents', 'claude-code.svg'), '<svg/>')
  writeFileSync(join(root, spec), 'test')
  const features = { features: [{ id: 'text-attachments', label: 'Text attachments', description: 'The file contents reach the agent.' }] }
  const checklist = {
    sharedNotes: [{ id: 1, text: 'The native path accepts text.' }],
    providerGroups: [[{ id: 'claude-code', label: 'Claude Code', icon: '/icons/agents/claude-code.svg', noteRefs: [] }]],
    cells: {
      'text-attachments': {
        'claude-code': {
          supported: true,
          notes: '',
          noteRefs: [1],
          spec,
          audit: 'covered',
          matrixVerified: true,
          testStatus: 'passed',
        },
      },
    },
  }
  const providerContract = { providers: { AGENT_PROVIDER_CLAUDE_CODE: { displayName: 'Claude Code' } } }
  return { root, features, checklist, providerContract, spec, cell: checklist.cells['text-attachments']['claude-code'] }
}

afterEach(() => {
  for (const directory of directories.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

describe('validateCodingAgentMatrix', () => {
  it('accepts a complete provider cell with its exact spec path', () => {
    const { root, features, checklist, providerContract } = fixture()
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true })).toEqual([])
  })

  it('rejects missing and unknown cells on both axes', () => {
    const { root, features, checklist, providerContract } = fixture()
    delete checklist.cells['text-attachments']['claude-code']
    checklist.cells['text-attachments'].unknown = {}
    checklist.cells.unknown = {}
    const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
    expect(errors).toContain('feature rows has unknown unknown')
    expect(errors).toContain('feature text-attachments provider cells is missing claude-code')
    expect(errors).toContain('feature text-attachments provider cells has unknown unknown')
  })

  it('rejects duplicate feature and provider IDs', () => {
    const { root, features, checklist, providerContract } = fixture()
    features.features.push({ id: 'text-attachments', label: 'Text attachments', description: 'Duplicate.' })
    checklist.providerGroups.push([{ id: 'claude-code', label: 'Claude Code', icon: '/icons/agents/claude-code.svg', noteRefs: [] }])
    const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
    expect(errors).toContain('feature ID occurs twice: text-attachments')
    expect(errors).toContain('provider ID occurs twice: claude-code')
  })

  it('compares the provider roster with its contract', () => {
    const { root, features, checklist, providerContract } = fixture()
    providerContract.providers.AGENT_PROVIDER_CODEX = { displayName: 'Codex' }
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('provider roster is missing Codex')
  })

  it('rejects a broken note reference and an unused shared note', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    cell.noteRefs = [2]
    const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
    expect(errors).toContain('note reference 2 has no shared note')
    expect(errors).toContain('shared note 1 has no provider or cell')
  })

  it('requires a passed spec to exist and a matrix claim to be verified', () => {
    const { root, features, checklist, providerContract, spec, cell } = fixture()
    unlinkSync(join(root, spec))
    cell.matrixVerified = false
    const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
    expect(errors).toContain('cell claude-code/text-attachments is not matrix-verified')
    expect(errors).toContain(`cell claude-code/text-attachments spec does not exist: ${spec}`)
    cell.spec = ''
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('passed cell claude-code/text-attachments has no spec path')
  })

  it('requires every supported cell to have a passed browser spec', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    cell.testStatus = 'pending'
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('supported cell claude-code/text-attachments has no passing browser spec')
  })

  it('requires the exact provider-feature filename in final migration mode', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    const wrong = 'frontend/tests/e2e/claude-code/wrong.spec.ts'
    writeFileSync(join(root, wrong), 'test')
    cell.spec = wrong
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true }))
      .toContain('cell claude-code/text-attachments must use frontend/tests/e2e/claude-code/text-attachments.spec.ts')
  })

  it('checks the full source grid and pins its feature IDs', () => {
    const { features, checklist, providerContract } = readCodingAgentMatrix()
    expect(features.features.map(feature => feature.id)).toEqual([
      'text-attachments',
      'image-attachments',
      'pdf-attachments',
      'other-binary-attachments',
      'images-in-tool-results',
      'thinking-in-the-transcript',
      'context-usage',
      'compaction-notice',
      'manual-compaction',
      'rate-limit-state',
      'model-context-on-resume',
      'permission-prompts',
      'plan-mode',
      'plan-approval-banner',
      'agent-questions',
      'mcp-tool-execution',
      'mcp-input-request',
      'smart-permissions-shortcut',
      'bypass-permissions-shortcut',
      'model',
      'reasoning-effort',
      'mode',
      'session-goal-set-and-clear',
      'session-goal-pause-and-resume',
      'to-do-sidebar',
      'background-tasks-sidebar',
      'subagent-transcript-tab',
      'subagent-live-transcript',
      'send-to-a-subagent',
      'interrupt-a-subagent',
      'steer-mid-turn',
      'workflow-grouping',
    ])
    expect(validateCodingAgentMatrix(features, checklist, providerContract)).toEqual([])
    expect(Object.values(checklist.cells).reduce((count, row) => count + Object.keys(row).length, 0)).toBe(832)
  })
})
