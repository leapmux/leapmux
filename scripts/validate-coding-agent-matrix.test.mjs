import { existsSync, mkdirSync, mkdtempSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
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
  const features = { features: [{ id: 'text-attachments', label: 'Text attachments', description: 'The file contents reach the agent.', showInMatrix: true }] }
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
          verified: true,
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
  it('uses one output file path spec for every provider', () => {
    const { features, checklist } = readCodingAgentMatrix()
    expect(features.features.find(feature => feature.id === 'output-file-paths'))
      .toEqual(expect.objectContaining({ label: 'Output file paths', showInMatrix: true }))
    const row = checklist.cells['output-file-paths']
    expect(row).toBeDefined()
    expect(checklist.cells).not.toHaveProperty(['tool-result-artifacts'])
    expect(checklist.cells).not.toHaveProperty(['full-tool-output'])
    const paths = new Set()
    for (const provider of checklist.providerGroups.flat()) {
      const expected = `frontend/tests/e2e/${provider.id}/output-file-paths.spec.ts`
      expect(row[provider.id].spec).toBe(expected)
      expect(existsSync(join(ROOT, expected))).toBe(true)
      expect(existsSync(join(ROOT, `frontend/tests/e2e/${provider.id}/tool-result-artifacts.spec.ts`))).toBe(false)
      expect(existsSync(join(ROOT, `frontend/tests/e2e/${provider.id}/full-tool-output.spec.ts`))).toBe(false)
      expect(paths.has(expected)).toBe(false)
      paths.add(expected)
    }
    expect(paths.size).toBe(checklist.providerGroups.flat().length)
  })

  it('accepts a complete provider cell with its exact spec path', () => {
    const { root, features, checklist, providerContract } = fixture()
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true })).toEqual([])
  })

  it('keeps the output path display label independent from its feature ID and spec path', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    features.features[0] = { id: 'output-file-paths', label: 'Output file paths after reload', description: 'LeapMux shows reported file paths beside the original preview after reload.', showInMatrix: true }
    const spec = 'frontend/tests/e2e/claude-code/output-file-paths.spec.ts'
    writeFileSync(join(root, spec), 'test')
    checklist.cells = { 'output-file-paths': { 'claude-code': { ...cell, spec } } }
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true })).toEqual([])
  })

  it.each(['', ' ', undefined])('rejects an absent display label: %j', (label) => {
    const { root, features, checklist, providerContract } = fixture()
    features.features[0].label = label
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('feature text-attachments has no display label')
  })

  it('requires an explicit website display flag on every feature', () => {
    const { root, features, checklist, providerContract } = fixture()
    delete features.features[0].showInMatrix
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('feature text-attachments has no boolean showInMatrix flag')
  })

  it('requires the verification field for published and test-only claims', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    delete cell.verified
    cell.matrixVerified = true
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('cell claude-code/text-attachments is not verified')
  })

  it('keeps a source-audited pending case in the work checklist', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    cell.testStatus = 'pending'
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true }))
      .toContain('cell claude-code/text-attachments spec has not passed')
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
    features.features.push({ id: 'text-attachments', label: 'Text attachments', description: 'Duplicate.', showInMatrix: true })
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
    cell.verified = false
    const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
    expect(errors).toContain('cell claude-code/text-attachments is not verified')
    expect(errors).toContain(`cell claude-code/text-attachments spec does not exist: ${spec}`)
    cell.spec = ''
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('passed cell claude-code/text-attachments has no spec path')
  })

  it('requires an evidence note for an unsupported cell in final migration mode', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    cell.supported = false
    cell.audit = 'covered-negative'
    cell.noteRefs = []
    checklist.sharedNotes = []
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true }))
      .toContain('unsupported cell claude-code/text-attachments has no evidence note')
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

  it('requires every provider cell in a test-only row', () => {
    const { root, features, checklist, providerContract } = fixture()
    features.features.push({ id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and answers.', showInMatrix: false })
    checklist.cells['basic-chat'] = {}
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
      .toContain('feature basic-chat provider cells is missing claude-code')
  })

  it('rejects a path shared by published and test-only cells in final mode', () => {
    const { root, features, checklist, providerContract, cell } = fixture()
    features.features.push({ id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and answers.', showInMatrix: false })
    checklist.cells['basic-chat'] = { 'claude-code': { ...cell } }
    expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true }))
      .toContain(`spec path serves more than one cell: ${cell.spec}`)
  })

  it('checks the full source grid and pins its feature IDs', () => {
    const { features, checklist, providerContract } = readCodingAgentMatrix()
    expect(features.features.filter(feature => feature.showInMatrix).map(feature => feature.id)).toEqual([
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
      'code-execution',
      'output-file-paths',
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
    expect(features.features.filter(feature => !feature.showInMatrix)).toHaveLength(19)
    expect(Object.values(checklist.cells).reduce((count, row) => count + Object.keys(row).length, 0))
      .toBe(features.features.length * checklist.providerGroups.flat().length)
  })
})
