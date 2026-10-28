import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { readCodingAgentMatrix, validateCodingAgentMatrix } from './validate-coding-agent-matrix.mjs'
import { buildAjv } from './validate-json.mjs'

const ROOT = resolve(import.meta.dirname, '..')
const SCRATCH_ROOT = join(ROOT, '.tmp')
const directories = []
const SUPPORT_STATES = [
  { id: 'supported', symbol: '✅', label: 'Supported' },
  { id: 'agent-limit', symbol: '🚫', label: 'The agent does not offer it' },
  { id: 'leapmux-limit', symbol: '🚧', label: 'LeapMux does not support it yet' },
]

function fixture() {
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  const root = mkdtempSync(join(SCRATCH_ROOT, 'matrix-validator-'))
  directories.push(root)
  const spec = 'frontend/tests/e2e/claude-code/text-attachments.spec.ts'
  mkdirSync(join(root, 'icons', 'agents'), { recursive: true })
  mkdirSync(join(root, 'frontend', 'tests', 'e2e', 'claude-code'), { recursive: true })
  writeFileSync(join(root, 'icons', 'agents', 'claude-code.svg'), '<svg/>')
  writeFileSync(join(root, spec), 'test')
  const features = {
    groups: [{ id: 'attachments', label: 'Attachments' }],
    features: [{ id: 'text-attachments', label: 'Text attachments', description: 'The file contents reach the agent.', showInMatrix: true, group: 'attachments' }],
  }
  const checklist = {
    supportStates: structuredClone(SUPPORT_STATES),
    providerGroups: [[{ id: 'claude-code', label: 'Claude Code', icon: '/icons/agents/claude-code.svg', userNote: '', detailNote: '' }]],
    cells: {
      'text-attachments': {
        'claude-code': {
          support: 'supported',
          userNote: '',
          detailNote: '',
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
    features.features[0] = { id: 'output-file-paths', label: 'Output file paths after reload', description: 'LeapMux shows reported file paths beside the original preview after reload.', showInMatrix: true, group: 'attachments' }
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
    checklist.providerGroups.push([{ id: 'claude-code', label: 'Claude Code', icon: '/icons/agents/claude-code.svg', userNote: '', detailNote: '' }])
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

  describe('support states and notes', () => {
    it('describes native Muse child sessions in the provider note', () => {
      const { checklist } = readCodingAgentMatrix()
      const provider = checklist.providerGroups.flat().find(provider => provider.id === 'muse-code')
      expect(provider.userNote).toContain('Muse Code 1.4.13')
      expect(provider.userNote).toContain('its own session')
      expect(provider.userNote).toContain('four approval modes')
      expect(provider.userNote).toContain('allowAll')
      expect(provider.userNote).toContain('no assisted preset')
    })

    it('describes the shared workflow row and native Muse child session', () => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells['workflow-grouping']['muse-code']
      expect(cell.userNote).toContain('matching native subagent')
      expect(cell.userNote).toContain('its own session')
      expect(cell.detailNote).toContain('childId, attempt')
      expect(cell.detailNote).toContain('subagentId')
      expect(cell.detailNote).toContain('childSessionId')
    })

    it('describes the five offered CodeBuddy permission modes', () => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells.mode['codebuddy-code']
      expect(cell.userNote).toContain('five')
      expect(cell.userNote).toContain('Auto')
      expect(cell.detailNote).toContain('auto')
      expect(cell.userNote).toContain('does not offer Don\'t Ask')
    })

    it('distinguishes native CodeBuddy questions from editor requests', () => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells['editor-requests']['codebuddy-code']
      expect(cell).toMatchObject({ support: 'agent-limit', audit: 'unsupported' })
      expect(cell.detailNote).toContain('AskUserQuestion')
      expect(cell.detailNote).toContain('can_use_tool')
      expect(cell.detailNote).toContain('updatedInput')
      expect(cell.detailNote).toContain('independent multiline editor request')
    })
    const hiddenFeature = { id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and answers.', showInMatrix: false }

    it('accepts a limited published cell that holds both notes', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.support = 'agent-limit'
      cell.audit = 'covered-negative'
      cell.userNote = 'Claude Code does not read text attachments. See [the issue](https://github.com/org/repo/issues/1).'
      cell.detailNote = 'The native protocol of version 1.0 has no text input. The issue https://github.com/org/repo/issues/1 asks for it, and the documentation lists no such block.'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true })).toEqual([])
    })

    it.each([
      [[]],
      [SUPPORT_STATES.slice(0, 2)],
      [[SUPPORT_STATES[1], SUPPORT_STATES[0], SUPPORT_STATES[2]]],
      [[SUPPORT_STATES[0], SUPPORT_STATES[1], { ...SUPPORT_STATES[2], id: 'other' }]],
    ])('rejects support states that differ from the three fixed ids in order: %j', (states) => {
      const { root, features, checklist, providerContract } = fixture()
      checklist.supportStates = states
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('supportStates must list supported, agent-limit, leapmux-limit in this order')
    })

    it('rejects a duplicate symbol or label, because the legend must tell the states apart', () => {
      const { root, features, checklist, providerContract } = fixture()
      checklist.supportStates[1].symbol = checklist.supportStates[0].symbol
      checklist.supportStates[2].label = checklist.supportStates[1].label
      const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
      expect(errors).toContain(`support state symbol occurs twice: ${SUPPORT_STATES[0].symbol}`)
      expect(errors).toContain(`support state label occurs twice: ${SUPPORT_STATES[1].label}`)
    })

    it('rejects a cell with an unknown support state', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.support = 'unsupported'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('cell claude-code/text-attachments has unknown support unsupported')
    })

    it.each(['agent-limit', 'leapmux-limit'])('requires a user note for a published cell that is %s', (support) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.support = support
      cell.audit = 'covered-negative'
      cell.detailNote = 'Evidence.'
      for (const note of ['', '  ']) {
        cell.userNote = note
        expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
          .toContain(`cell claude-code/text-attachments is ${support} but has no user note`)
      }
    })

    it.each(['agent-limit', 'leapmux-limit'])('requires a detail note for a cell that is %s, in every mode', (support) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.support = support
      cell.audit = 'covered-negative'
      cell.userNote = 'A note for the reader.'
      for (const note of ['', '  ']) {
        cell.detailNote = note
        expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
          .toContain(`cell claude-code/text-attachments is ${support} but has no detail note`)
        expect(validateCodingAgentMatrix(features, checklist, providerContract, { root, requireCellSpecs: true }))
          .toContain(`cell claude-code/text-attachments is ${support} but has no detail note`)
      }
    })

    it('lets a supported cell keep empty notes', () => {
      const { root, features, checklist, providerContract } = fixture()
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    })

    it('rejects a user note on a hidden feature, because nothing publishes it', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      features.features.push(hiddenFeature)
      checklist.cells['basic-chat'] = { 'claude-code': { ...cell, userNote: 'Never shown.' } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('cell claude-code/basic-chat belongs to a hidden feature and must have no user note')
    })

    it('accepts a detail note on a hidden feature', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      features.features.push(hiddenFeature)
      checklist.cells['basic-chat'] = { 'claude-code': { ...cell, support: 'agent-limit', audit: 'covered-negative', detailNote: 'Evidence only.' } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    })

    it.each([
      ['a frontend path', 'See frontend/src/app.ts for the code.'],
      ['a backend path', 'The code lives in backend/internal/worker.'],
      ['a spec file name', 'The test is text-attachments.spec.ts.'],
      ['a Go file name', 'The handler is in agent.go.'],
      ['a TypeScript file name', 'The extractor is in plugin.tsx.'],
      ['a path after a parenthesis', 'It reads a file (scripts/validate.mjs).'],
      ['a path in quotation marks', 'The code lives in "backend/internal/worker".'],
      ['a path in brackets', 'The code lives in [backend/internal/worker].'],
      ['a path in backticks', 'The code lives in `backend/internal/worker`.'],
      ['a path without a top directory', 'The code lives in internal/worker/agent.'],
      ['a directory with a trailing slash', 'The files live in scripts/.'],
      ['a data file in a repository directory', 'The data is in contracts/providers.json.'],
      ['a protocol buffer file name', 'The schema is worker.proto.'],
      ['a Rust file name', 'The code is in main.rs.'],
      ['a Python file name', 'The helper is sync.py.'],
      ['a JavaScript file name', 'The loader is index.js.'],
      ['a JavaScript module file name', 'The script is build.cjs.'],
    ])('rejects a user note that holds %s', (_name, note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments holds a repository path or a source file name')
    })

    it('rejects a user note that holds an http:// link', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = 'See [the page](http://example.com/issue/1).'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments holds an http:// link')
    })

    it.each([
      'Edit `settings.json` to turn it on.',
      'See [the issue](https://github.com/org/repo/issues/1).',
      'The Go docs are at [pkg.go.dev](https://pkg.go.dev/example).',
      'Run `/compact` first.',
      'The frontend/backend split stays hidden.',
      'It runs shell scripts/hooks before a turn.',
      'It needs Node.js 20 or later.',
      'Call `tools.<name>(arguments)` with `--no-extensions`.',
      'Set the endpoint to `https://api.example.com/v1`.',
      'The answer is 2 < 3 and 5 > 4.',
    ])('accepts a user note that a reader can use: %s', (note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    })

    it('applies the user note rules to a provider note', () => {
      const { root, features, checklist, providerContract } = fixture()
      checklist.providerGroups[0][0].userNote = 'See backend/internal/x and http://example.com.'
      const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
      expect(errors).toContain('user note of provider claude-code holds a repository path or a source file name')
      expect(errors).toContain('user note of provider claude-code holds an http:// link')
    })

    it.each([
      ['a path', 'See [the code](backend/internal/worker/x.go).', 'backend/internal/worker/x.go'],
      ['a javascript: URL', 'See [the page](javascript:alert(1)).', 'javascript:alert(1)'],
      ['an ftp:// URL', 'See [the file](ftp://example.com/file).', 'ftp://example.com/file'],
      ['a scheme-relative URL', 'See [the page](//example.com/page).', '//example.com/page'],
      ['an in-page anchor', 'See [the note](#note-pi-model).', '#note-pi-model'],
      ['an entity that spells javascript:', 'See [the page](&#106;avascript:alert(1)).', 'javascript:alert(1)'],
    ])('rejects a user note that links to %s', (_name, note, target) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain(`user note of cell claude-code/text-attachments links to ${JSON.stringify(target)}, which is not an https:// URL`)
    })

    it.each([
      ['inline HTML', 'Press <b>Stop</b> to end it.'],
      ['a script element', 'Press it.<script>alert(1)</script>'],
      ['a block of HTML', '<img src=x onerror=alert(1)>'],
      ['an HTML comment', 'Press it. <!-- hidden --> Then wait.'],
    ])('rejects a user note that holds raw HTML: %s', (_name, note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments holds raw HTML')
    })

    it('rejects a user note that embeds an image, because the page would load a remote file', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = 'See ![the chart](https://example.com/chart.png).'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments holds an image')
    })

    it.each([
      'See ftp://example.com/file for it.',
      'See www.example.com for it.',
      'See https://example.com/page for it.',
    ])('rejects a user note that holds a bare URL, because the website turns it into a link: %s', (note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments holds a bare URL; write a Markdown link')
    })

    it.each([
      ['a list', '- one'],
      ['a heading', '# Title'],
      ['a block quote', '> quoted'],
      ['an indented code block', '    code'],
      ['two paragraphs', 'one\r\rtwo'],
    ])('rejects a user note that is not one paragraph: %s', (_name, note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments must be one paragraph of text')
    })

    it.each(['  ', '\t', ' '])('rejects a user note that holds only whitespace, because the website shows an empty note: %j', (note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments holds only whitespace; leave it empty')
      cell.userNote = ''
      checklist.providerGroups[0][0].userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of provider claude-code holds only whitespace; leave it empty')
    })

    it('rejects a whitespace-only user note on a hidden feature', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      features.features.push(hiddenFeature)
      checklist.cells['basic-chat'] = { 'claude-code': { ...cell, userNote: '  ' } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/basic-chat holds only whitespace; leave it empty')
    })

    it.each([[5], [null], [undefined], [['note']], [{ text: 'note' }]])('reports a user note that is not a string, and does not throw: %j', (note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.userNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments is not a string')
      cell.support = 'agent-limit'
      cell.audit = 'covered-negative'
      cell.detailNote = 'Evidence for the maintainers.'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('user note of cell claude-code/text-attachments is not a string')
    })

    it.each([[5], [null], [undefined], [['note']], [{ text: 'note' }]])('reports a detail note that is not a string, and does not throw: %j', (note) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.detailNote = note
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('detail note of cell claude-code/text-attachments is not a string')
      cell.support = 'leapmux-limit'
      cell.audit = 'refusal-covered'
      cell.userNote = 'A note for the reader.'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('detail note of cell claude-code/text-attachments is not a string')
    })

    it.each([
      ['equals its user note', 'A note for the reader.'],
      ['is shorter than its user note', 'x'],
    ])('rejects a limited cell whose detail note %s, because the detail note holds the evidence', (_name, detail) => {
      const { root, features, checklist, providerContract, cell } = fixture()
      cell.support = 'agent-limit'
      cell.audit = 'covered-negative'
      cell.userNote = 'A note for the reader.'
      cell.detailNote = detail
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('cell claude-code/text-attachments is agent-limit but its detail note is not longer than its user note')
    })

    it.each(['opencode', 'kilo'])('records native completed compaction support for %s', (provider) => {
      const { checklist } = readCodingAgentMatrix()
      expect(checklist.cells['compaction-notice'][provider]).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: `frontend/tests/e2e/${provider}/compaction-notice.spec.ts`,
      })
    })

    it.each([
      [
        'records native Agent Teams support for Cline',
        'swarm-mode',
        'cline',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/cline/swarm-mode.spec.ts',
        },
        [],
      ],
      [
        'records native plan approval support for Reasonix',
        'plan-approval-banner',
        'reasonix',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/reasonix/plan-approval-banner.spec.ts',
        },
        [],
      ],
      [
        'records native token progress support for Qoder CLI',
        'generation-progress',
        'qoder-cli',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qoder-cli/generation-progress.spec.ts',
        },
        [],
      ],
      [
        'records native Kilo Swarm support for Kilo',
        'swarm-mode',
        'kilo',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/kilo/swarm-mode.spec.ts',
        },
        [],
      ],
      [
        'records native plan usage support for Kilo',
        'rate-limit-state',
        'kilo',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/kilo/rate-limit-state.spec.ts',
        },
        [],
      ],
      [
        'records the native Smart permissions shortcut for CodeBuddy Code',
        'smart-permissions-shortcut',
        'codebuddy-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/codebuddy-code/smart-permissions-shortcut.spec.ts',
        },
        [],
      ],
      [
        'records the Auto review Smart permissions shortcut for DeepSeek Harness',
        'smart-permissions-shortcut',
        'deepseek-harness',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/deepseek-harness/smart-permissions-shortcut.spec.ts',
        },
        [],
      ],
      [
        'records the CodeBuddy Code MCP form refusal as an agent limit',
        'mcp-input-request',
        'codebuddy-code',
        {
          support: 'agent-limit',
          audit: 'unsupported',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/codebuddy-code/mcp-input-request.spec.ts',
        },
        [],
      ],
      [
        'records native workflow grouping for Claude Code',
        'workflow-grouping',
        'claude-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/claude-code/workflow-grouping.spec.ts',
        },
        [],
      ],
      [
        'records native workflow grouping for Qwen Code',
        'workflow-grouping',
        'qwen-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qwen-code/workflow-grouping.spec.ts',
        },
        [],
      ],
      [
        'records the native output style for Qwen Code',
        'output-style',
        'qwen-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qwen-code/output-style.spec.ts',
        },
        [],
      ],
      [
        'records the native restriction on Copilot factories and dynamic workflows',
        'workflow-grouping',
        'github-copilot',
        {
          support: 'agent-limit',
          audit: 'unsupported',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/github-copilot/workflow-grouping.spec.ts',
        },
        [],
      ],
      [
        'records native account usage and calendar reset dates for Kiro',
        'rate-limit-state',
        'kiro',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/kiro/rate-limit-state.spec.ts',
        },
        [],
      ],
      [
        'records native Word and Excel attachments for Kiro',
        'other-binary-attachments',
        'kiro',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/kiro/other-binary-attachments.spec.ts',
        },
        [],
      ],
      [
        'records native audio attachments for Codex',
        'audio-attachments',
        'codex',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/codex/audio-attachments.spec.ts',
        },
        [],
      ],
      [
        'records native messages to a running Grok Build subagent',
        'send-to-a-subagent',
        'grok-build',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/grok-build/send-to-a-subagent.spec.ts',
        },
        [],
      ],
      [
        'records native Swarm mode for CodeBuddy Code',
        'swarm-mode',
        'codebuddy-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/codebuddy-code/swarm-mode.spec.ts',
        },
        [],
      ],
      [
        'records native mid-turn steering for Junie',
        'steer-mid-turn',
        'junie',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/junie/steer-mid-turn.spec.ts',
        },
        [],
      ],
      [
        'records native Operate orchestration for Codewhale',
        'swarm-mode',
        'codewhale',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/codewhale/swarm-mode.spec.ts',
        },
        [],
      ],
      [
        'records native output file paths for Fast Agent',
        'output-file-paths',
        'fast-agent',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/fast-agent/output-file-paths.spec.ts',
        },
        [],
      ],
      [
        'records native output file paths for Goose',
        'output-file-paths',
        'goose',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/goose/output-file-paths.spec.ts',
        },
        [],
      ],
      [
        'records native mid-turn steering for Goose',
        'steer-mid-turn',
        'goose',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/goose/steer-mid-turn.spec.ts',
        },
        [],
      ],
      [
        'records the headless output style refusal of Factory Droid as an agent limit',
        'output-style',
        'factory-droid',
        {
          support: 'agent-limit',
          audit: 'unsupported',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/factory-droid/output-style.spec.ts',
        },
        [],
      ],
      [
        'records native workflow grouping for ZCode',
        'workflow-grouping',
        'zcode',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/zcode/workflow-grouping.spec.ts',
        },
        [],
      ],
      [
        'records hosted hook trust for ZCode',
        'workspace-trust',
        'zcode',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/zcode/workspace-trust.spec.ts',
        },
        [],
      ],
      [
        'records native PDF attachment support for ZCode',
        'pdf-attachments',
        'zcode',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/zcode/pdf-attachments.spec.ts',
        },
        [],
      ],
      [
        'records background subagent interrupt support for ZCode',
        'interrupt-a-subagent',
        'zcode',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/zcode/interrupt-a-subagent.spec.ts',
        },
        [],
      ],
      [
        'records typed MCP form support for ZCode',
        'mcp-input-request',
        'zcode',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/zcode/mcp-input-request.spec.ts',
        },
        [],
      ],
      [
        'records native steering support for ZCode',
        'steer-mid-turn',
        'zcode',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/zcode/steer-mid-turn.spec.ts',
        },
        [],
      ],
      [
        'records plan usage support for Kimi Code',
        'rate-limit-state',
        'kimi-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/kimi-code/rate-limit-state.spec.ts',
        },
        [],
      ],
      [
        'records hosted workspace trust support for Kimi Code',
        'workspace-trust',
        'kimi-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/kimi-code/workspace-trust.spec.ts',
        },
        [],
      ],
      [
        'records hosted workspace trust support for GitHub Copilot',
        'workspace-trust',
        'github-copilot',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/github-copilot/workspace-trust.spec.ts',
        },
        [],
      ],
      [
        'records native Fleet support for GitHub Copilot',
        'swarm-mode',
        'github-copilot',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/github-copilot/swarm-mode.spec.ts',
        },
        [],
      ],
      [
        'records native PDF attachment support for GitHub Copilot',
        'pdf-attachments',
        'github-copilot',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/github-copilot/pdf-attachments.spec.ts',
        },
        [],
      ],
      [
        'records native subagent interrupt support for Qoder CLI',
        'interrupt-a-subagent',
        'qoder-cli',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qoder-cli/interrupt-a-subagent.spec.ts',
        },
        [],
      ],
      [
        'records native account quota support for Qoder CLI',
        'rate-limit-state',
        'qoder-cli',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qoder-cli/rate-limit-state.spec.ts',
        },
        [],
      ],
      [
        'records native output style support for Qoder CLI',
        'output-style',
        'qoder-cli',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qoder-cli/output-style.spec.ts',
        },
        [],
      ],
      [
        'records native extended thinking support for Qoder CLI',
        'extended-thinking',
        'qoder-cli',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qoder-cli/extended-thinking.spec.ts',
        },
        [],
      ],
      [
        'records native Bypass permissions support for Qoder CLI',
        'bypass-permissions-shortcut',
        'qoder-cli',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/qoder-cli/bypass-permissions-shortcut.spec.ts',
        },
        [],
      ],
      [
        'records native Muse subagent-transcript-tab support',
        'subagent-transcript-tab',
        'muse-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/muse-code/subagent-transcript-tab.spec.ts',
        },
        ['Muse 1.4.13', 'view/page'],
      ],
      [
        'records native Muse subagent-live-transcript support',
        'subagent-live-transcript',
        'muse-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/muse-code/subagent-live-transcript.spec.ts',
        },
        ['Muse 1.4.13', 'view/subscribe'],
      ],
      [
        'records native Muse send-to-a-subagent support',
        'send-to-a-subagent',
        'muse-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/muse-code/send-to-a-subagent.spec.ts',
        },
        ['Muse 1.4.13', 'subagent/sendMessage', 'subagent_state_ineligible', 'subagent/readResult', 'paused input queue'],
      ],
      [
        'records native Muse interrupt-a-subagent support',
        'interrupt-a-subagent',
        'muse-code',
        {
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: 'frontend/tests/e2e/muse-code/interrupt-a-subagent.spec.ts',
        },
        ['Muse 1.4.13', 'subagent/interrupt'],
      ],
    ])('%s', (_title, feature, provider, expected, detailContains) => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells[feature]?.[provider]
      expect(cell).toMatchObject(expected)
      for (const fragment of detailContains)
        expect(cell.detailNote).toContain(fragment)
    })

    it('records native subagent live transcript and send support for OpenCode and Kilo', () => {
      const { checklist } = readCodingAgentMatrix()
      for (const feature of ['subagent-live-transcript', 'send-to-a-subagent']) {
        for (const provider of ['opencode', 'kilo']) {
          expect(checklist.cells[feature][provider]).toMatchObject({
            support: 'supported',
            audit: 'source-verified',
            testStatus: 'passed',
            verified: true,
            spec: `frontend/tests/e2e/${provider}/${feature}.spec.ts`,
          })
        }
      }
    })

    it('records native Bypass permissions support for OpenCode and Kilo', () => {
      const { checklist } = readCodingAgentMatrix()
      for (const provider of ['opencode', 'kilo']) {
        expect(checklist.cells['bypass-permissions-shortcut'][provider]).toMatchObject({
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: `frontend/tests/e2e/${provider}/bypass-permissions-shortcut.spec.ts`,
        })
      }
    })

    it('records native subagent interrupt support for OpenCode and Kilo', () => {
      const { checklist } = readCodingAgentMatrix()
      for (const provider of ['opencode', 'kilo']) {
        expect(checklist.cells['interrupt-a-subagent'][provider]).toMatchObject({
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: `frontend/tests/e2e/${provider}/interrupt-a-subagent.spec.ts`,
        })
      }
    })

    it.each([
      ['claude-code', 'agent-limit', 'Messages API'],
      ['codex', 'supported', 'input_audio.audio_url'],
      ['cursor', 'agent-limit', 'audit-8D7OZV'],
      ['github-copilot', 'agent-limit', 'copilot-audit-1.log'],
      ['kilo', 'supported', 'capabilities.input.audio'],
      ['opencode', 'supported', 'capabilities.input.audio'],
      ['goose', 'agent-limit', 'convert_acp_prompt_to_message'],
      ['pi', 'agent-limit', 'pi-6wxxro'],
      ['reasonix', 'agent-limit', 'FlattenPrompt'],
      ['zcode', 'agent-limit', 'zcode-PbCD0M'],
      ['codewhale', 'agent-limit', 'magic-format'],
      ['kimi-code', 'agent-limit', 'ReadMediaFile'],
      ['mimo-code', 'supported', 'capabilities.input.audio'],
      ['qwen-code', 'supported', 'run-rUPUPT'],
      ['oh-my-pi', 'agent-limit', 'pi-6wxxro'],
      ['grok-build', 'agent-limit', '-32602'],
      ['kiro', 'agent-limit', 'audit-8D7OZV'],
      ['amp', 'agent-limit', 'message.content.1'],
      ['cline', 'agent-limit', 'Cannot read binary file into context'],
      ['codebuddy-code', 'supported', 'file.file_data'],
      ['junie', 'agent-limit', 'audit-8D7OZV'],
      ['letta-code', 'agent-limit', 'letta-lR905P'],
      ['dirac', 'agent-limit', 'audit-l1x00T'],
      ['qoder-cli', 'agent-limit', 'stream-aHFbCW'],
      ['factory-droid', 'agent-limit', 'params.files.0'],
      ['fast-agent', 'supported', '/model switch'],
      ['command-code', 'agent-limit', '-32602'],
      ['deepseek-harness', 'agent-limit', 'gateway/input-invalid'],
      ['gemini-cli', 'supported', 'audit-zeZZxP'],
      ['muse-code', 'agent-limit', 'UUIDv7'],
    ])('records native audio attachment evidence for %s', (provider, support, evidence) => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells['audio-attachments']?.[provider]
      expect(cell).toMatchObject({
        support,
        audit: support === 'supported' ? 'source-verified' : 'unsupported',
        verified: true,
        testStatus: 'passed',
        spec: `frontend/tests/e2e/${provider}/audio-attachments.spec.ts`,
      })
      expect(cell?.detailNote).toContain(evidence)
      expect(cell?.userNote.length).toBeGreaterThan(0)
    })

    it('publishes audio apart from other binary attachments for every provider', () => {
      const { checklist, features } = readCodingAgentMatrix()
      const audio = features.features.find(feature => feature.id === 'audio-attachments')
      expect(audio).toMatchObject({ label: 'Audio attachments', showInMatrix: true, group: 'attachments' })
      expect(audio?.description).toContain('A text transcript alone does not count')
      expect(features.features.find(feature => feature.id === 'other-binary-attachments')?.description)
        .toContain('other than an image, PDF or audio recording')
      const providerIDs = checklist.providerGroups.flat().map(provider => provider.id)
      expect(Object.keys(checklist.cells['audio-attachments'] ?? {})).toEqual(providerIDs)
      expect(features.features.filter(feature => feature.group === 'attachments').map(feature => feature.id))
        .toEqual(['text-attachments', 'image-attachments', 'pdf-attachments', 'audio-attachments', 'other-binary-attachments'])
    })

    it('records the native Codex refusal of non-audio binary input', () => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells['other-binary-attachments'].codex
      expect(cell).toMatchObject({ support: 'agent-limit', audit: 'unsupported', verified: true, testStatus: 'passed', spec: 'frontend/tests/e2e/codex/other-binary-attachments.spec.ts' })
      expect(cell.detailNote).toContain('0.164.0')
      expect(cell.detailNote).toContain('-32600')
      expect(cell.detailNote).toContain('run-CtskPt')
      expect(cell.userNote).not.toContain('LeapMux does not send it yet')
    })

    it('records an independent native Gemini video attachment proof', () => {
      const { checklist } = readCodingAgentMatrix()
      const cell = checklist.cells['other-binary-attachments']['gemini-cli']
      expect(cell).toMatchObject({ support: 'supported', audit: 'source-verified', verified: true, testStatus: 'passed' })
      expect(cell.detailNote).toContain('MP4')
      expect(cell.detailNote).toContain('0.65.0')
      expect(cell.userNote).toContain('video')
    })

    it('records native subagent send and interrupt support for GitHub Copilot', () => {
      const { checklist } = readCodingAgentMatrix()
      for (const feature of ['send-to-a-subagent', 'interrupt-a-subagent']) {
        expect(checklist.cells[feature]['github-copilot']).toMatchObject({
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: `frontend/tests/e2e/github-copilot/${feature}.spec.ts`,
        })
      }
    })

    it('records the audited support totals and the native question and compaction corrections', () => {
      const { features, checklist, providerContract } = readCodingAgentMatrix()
      expect(validateCodingAgentMatrix(features, checklist, providerContract)).toEqual([])
      expect(checklist.cells['extended-thinking'].cline).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/cline/extended-thinking.spec.ts',
      })
      expect(checklist.cells['extended-thinking']['codebuddy-code']).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/codebuddy-code/extended-thinking.spec.ts',
      })
      expect(checklist.cells['output-style']['codebuddy-code']).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/codebuddy-code/output-style.spec.ts',
      })
      const counts = { 'supported': 0, 'agent-limit': 0, 'leapmux-limit': 0 }
      for (const row of Object.values(checklist.cells)) {
        for (const cell of Object.values(row))
          counts[cell.support] += 1
      }
      // A deliberate change of a verdict changes these numbers. Update them with the checklist.
      expect(counts).toEqual({ 'supported': 1180, 'agent-limit': 440, 'leapmux-limit': 0 })
      expect(checklist.cells['agent-questions']['codebuddy-code']).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/codebuddy-code/agent-questions.spec.ts',
      })
      expect(checklist.cells['rate-limit-state']['letta-code']).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/letta-code/rate-limit-state.spec.ts',
      })
      expect(checklist.cells['reasoning-effort']['letta-code']).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/letta-code/reasoning-effort.spec.ts',
      })
      expect(checklist.cells['compaction-notice'].reasonix).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/reasonix/compaction-notice.spec.ts',
      })
      expect(checklist.cells['bypass-permissions-shortcut']['fast-agent']).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/fast-agent/bypass-permissions-shortcut.spec.ts',
      })
      expect(checklist.cells['bypass-permissions-shortcut'].dirac).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/dirac/bypass-permissions-shortcut.spec.ts',
      })
      expect(checklist.cells['fast-mode'].amp).toMatchObject({
        support: 'supported',
        audit: 'source-verified',
        testStatus: 'passed',
        verified: true,
        spec: 'frontend/tests/e2e/amp/fast-mode.spec.ts',
      })
      for (const feature of ['extended-thinking', 'fast-mode']) {
        expect(checklist.cells[feature].cursor).toMatchObject({
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: `frontend/tests/e2e/cursor/${feature}.spec.ts`,
        })
      }
      for (const feature of ['session-goal-set-and-clear', 'session-goal-pause-and-resume', 'send-to-a-subagent', 'interrupt-a-subagent', 'output-style', 'fast-mode']) {
        expect(checklist.cells[feature]['oh-my-pi']).toMatchObject({
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: `frontend/tests/e2e/oh-my-pi/${feature}.spec.ts`,
        })
      }
      for (const feature of ['smart-permissions-shortcut', 'bypass-permissions-shortcut']) {
        expect(checklist.cells[feature].junie).toMatchObject({
          support: 'supported',
          audit: 'source-verified',
          testStatus: 'passed',
          verified: true,
          spec: `frontend/tests/e2e/junie/${feature}.spec.ts`,
        })
      }
      for (const feature of ['session-goal-set-and-clear', 'session-goal-pause-and-resume'])
        expect(checklist.cells[feature]['codebuddy-code']).toMatchObject({ support: 'agent-limit', audit: 'unsupported', testStatus: 'passed' })
      expect(checklist.cells['images-in-tool-results'].cline).toMatchObject({ support: 'supported', audit: 'covered', testStatus: 'passed' })
      for (const provider of ['qwen-code', 'grok-build', 'kiro'])
        expect(checklist.cells['compaction-notice'][provider].support).toBe('supported')
      expect(checklist.cells['agent-questions'].reasonix.support).toBe('supported')
      for (const provider of ['letta-code', 'amp', 'command-code'])
        expect(checklist.cells['code-execution'][provider].support).toBe('supported')
      expect(checklist.cells['model-context-on-resume'].dirac.support).toBe('supported')
      expect(checklist.cells['smart-permissions-shortcut'].codex.support).toBe('supported')
      expect(checklist.cells['extended-thinking']['claude-code']).toMatchObject({ support: 'agent-limit', audit: 'unsupported', testStatus: 'passed' })
      expect(checklist.supportStates).toEqual(SUPPORT_STATES)
    })
  })

  describe('the final migration rule in the build', () => {
    const tasks = Bun.YAML.parse(readFileSync(join(ROOT, 'Taskfile.yaml'), 'utf8')).tasks

    it('runs the matrix validator in final mode inside validate-json', () => {
      expect(tasks['validate-json'].cmds).toContain('bun scripts/validate-coding-agent-matrix.mjs --require-cell-specs')
    })

    it('re-runs validate-json when a spec file, an icon, or the note parser changes', () => {
      const sources = tasks['validate-json'].sources
      expect(sources).toContain('frontend/tests/e2e/*/*.spec.ts')
      expect(sources).toContain('icons/agents/*.svg')
      expect(sources).toContain('scripts/matrix-markdown.mjs')
    })
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

  describe('feature groups', () => {
    const twoGroups = (features) => {
      features.groups = [{ id: 'attachments', label: 'Attachments' }, { id: 'tools', label: 'Tools' }]
    }
    const published = (id, group) => ({ id, label: id, description: `The ${id} feature.`, showInMatrix: true, group })

    it('requires every published feature to name a known group', () => {
      const { root, features, checklist, providerContract } = fixture()
      delete features.features[0].group
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('feature text-attachments has no group')
      features.features[0].group = 'nowhere'
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('feature text-attachments has unknown group nowhere')
    })

    it('rejects a group on a hidden feature, because nothing displays it', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      features.features.push({ id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and answers.', showInMatrix: false, group: 'attachments' })
      checklist.cells['basic-chat'] = { 'claude-code': { ...cell } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('hidden feature basic-chat must not have a group')
    })

    it('accepts a hidden feature without a group', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      features.features.push({ id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and answers.', showInMatrix: false })
      checklist.cells['basic-chat'] = { 'claude-code': { ...cell, spec: 'frontend/tests/e2e/claude-code/text-attachments.spec.ts' } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root })).toEqual([])
    })

    it('rejects a group that holds no published feature', () => {
      const { root, features, checklist, providerContract } = fixture()
      twoGroups(features)
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('group tools has no published feature')
    })

    it('rejects duplicate group IDs and labels', () => {
      const { root, features, checklist, providerContract } = fixture()
      features.groups.push({ id: 'attachments', label: 'Attachments' })
      const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
      expect(errors).toContain('group ID occurs twice: attachments')
      expect(errors).toContain('group label occurs twice: Attachments')
    })

    it.each(['', ' ', undefined])('rejects an absent group label: %j', (label) => {
      const { root, features, checklist, providerContract } = fixture()
      features.groups[0].label = label
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('group attachments has no display label')
    })

    it('requires the published features in group order', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      twoGroups(features)
      features.features.unshift(published('code-execution', 'tools'))
      checklist.cells['code-execution'] = { 'claude-code': { ...cell } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('feature text-attachments (group attachments) follows a feature of a later group; order the published features by group')
    })

    it('rejects a group that another group splits', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      twoGroups(features)
      features.features.push(published('code-execution', 'tools'), published('pdf-attachments', 'attachments'))
      for (const id of ['code-execution', 'pdf-attachments'])
        checklist.cells[id] = { 'claude-code': { ...cell } }
      expect(validateCodingAgentMatrix(features, checklist, providerContract, { root }))
        .toContain('feature pdf-attachments (group attachments) follows a feature of a later group; order the published features by group')
    })

    it('lets a hidden feature sit inside a group without splitting it', () => {
      const { root, features, checklist, providerContract, cell } = fixture()
      features.features.push(
        { id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and answers.', showInMatrix: false },
        published('pdf-attachments', 'attachments'),
      )
      for (const id of ['basic-chat', 'pdf-attachments'])
        checklist.cells[id] = { 'claude-code': { ...cell } }
      const errors = validateCodingAgentMatrix(features, checklist, providerContract, { root })
      expect(errors.filter(error => error.includes('group'))).toEqual([])
    })
  })

  it('checks the full source grid and pins its feature IDs', () => {
    const { features, checklist, providerContract } = readCodingAgentMatrix()
    expect(features.groups.map(group => [group.id, group.label])).toEqual([
      ['attachments', 'Attachments'],
      ['conversation', 'Conversation'],
      ['context', 'Context'],
      ['models', 'Models and settings'],
      ['permissions', 'Permissions'],
      ['planning', 'Planning and goals'],
      ['tools', 'Tools'],
      ['subagents', 'Subagents'],
    ])
    expect(features.features.filter(feature => feature.showInMatrix).map(feature => [feature.group, feature.id])).toEqual([
      ['attachments', 'text-attachments'],
      ['attachments', 'image-attachments'],
      ['attachments', 'pdf-attachments'],
      ['attachments', 'audio-attachments'],
      ['attachments', 'other-binary-attachments'],
      ['conversation', 'thinking-in-the-transcript'],
      ['conversation', 'steer-mid-turn'],
      ['conversation', 'agent-questions'],
      ['context', 'context-usage'],
      ['context', 'compaction-notice'],
      ['context', 'manual-compaction'],
      ['context', 'model-context-on-resume'],
      ['context', 'rate-limit-state'],
      ['models', 'model'],
      ['models', 'reasoning-effort'],
      ['models', 'mode'],
      ['permissions', 'permission-prompts'],
      ['permissions', 'smart-permissions-shortcut'],
      ['permissions', 'bypass-permissions-shortcut'],
      ['planning', 'plan-mode'],
      ['planning', 'plan-approval-banner'],
      ['planning', 'session-goal-set-and-clear'],
      ['planning', 'session-goal-pause-and-resume'],
      ['planning', 'to-do-sidebar'],
      ['planning', 'background-tasks-sidebar'],
      ['tools', 'mcp-tool-execution'],
      ['tools', 'mcp-input-request'],
      ['tools', 'code-execution'],
      ['tools', 'output-file-paths'],
      ['tools', 'images-in-tool-results'],
      ['subagents', 'subagent-transcript-tab'],
      ['subagents', 'subagent-live-transcript'],
      ['subagents', 'send-to-a-subagent'],
      ['subagents', 'interrupt-a-subagent'],
      ['subagents', 'workflow-grouping'],
    ])
    expect(validateCodingAgentMatrix(features, checklist, providerContract)).toEqual([])
    expect(features.features.filter(feature => !feature.showInMatrix)).toHaveLength(19)
    expect(Object.values(checklist.cells).reduce((count, row) => count + Object.keys(row).length, 0))
      .toBe(features.features.length * checklist.providerGroups.flat().length)
  })
})

describe('features.schema.json', () => {
  const schema = JSON.parse(readFileSync(join(ROOT, 'frontend/tests/e2e/feature-matrix/features.schema.json'), 'utf8'))
  const validate = buildAjv().compile(schema)
  const published = { id: 'pdf-attachments', label: 'PDF attachments', description: 'The agent reads a PDF.', showInMatrix: true, group: 'attachments' }
  const hidden = { id: 'basic-chat', label: 'Basic chat', description: 'The agent answers.', showInMatrix: false }
  const document = features => ({ _readme: 'Feature definitions.', groups: [{ id: 'attachments', label: 'Attachments' }], features })
  /** Each failure as `keyword path detail`, so a test identifies the rule that rejects. */
  const failures = (data) => {
    validate(data)
    return (validate.errors ?? []).map(error => `${error.keyword} ${error.instancePath} ${error.params.missingProperty ?? ''}`.trim())
  }

  it('accepts a published feature with a group and a hidden feature without one', () => {
    expect(failures(document([published, hidden]))).toEqual([])
  })

  it('requires a group on a published feature', () => {
    const { group, ...withoutGroup } = published
    expect(failures(document([withoutGroup]))).toContain('required /features/0 group')
  })

  it('forbids a group on a hidden feature', () => {
    expect(failures(document([{ ...hidden, group: 'attachments' }]))).toContain('false schema /features/0/group')
  })

  it('requires the groups array and each group label', () => {
    expect(failures({ _readme: 'x', features: [hidden] })).toContain('required  groups')
    expect(failures({ ...document([published]), groups: [{ id: 'attachments', label: '' }] })).toContain('minLength /groups/0/label')
    expect(failures({ ...document([published]), groups: [] })).toContain('minItems /groups')
  })

  it('rejects a group ID that is not kebab-case', () => {
    expect(failures({ ...document([published]), groups: [{ id: 'Not Kebab', label: 'x' }] })).toContain('pattern /groups/0/id')
    expect(failures(document([{ ...published, group: 'Not Kebab' }]))).toContain('pattern /features/0/group')
  })
})

describe('checklist.schema.json', () => {
  const schema = JSON.parse(readFileSync(join(ROOT, 'frontend/tests/e2e/feature-matrix/checklist.schema.json'), 'utf8'))
  const validate = buildAjv().compile(schema)
  const provider = { id: 'claude-code', label: 'Claude Code', url: 'https://claude.com/', icon: '/icons/agents/claude-code.svg', userNote: '', detailNote: '' }
  const cell = { support: 'supported', userNote: '', detailNote: '', spec: '', audit: 'covered', verified: true, testStatus: 'pending' }
  const document = (overrides = {}, cellOverrides = {}, providerOverrides = {}) => ({
    _readme: 'The checklist.',
    supportStates: SUPPORT_STATES,
    providerGroups: [[{ ...provider, ...providerOverrides }]],
    cells: { 'text-attachments': { 'claude-code': { ...cell, ...cellOverrides } } },
    ...overrides,
  })
  /** Each failure as `keyword path detail`, so a test identifies the rule that rejects. */
  const failures = (data) => {
    validate(data)
    return (validate.errors ?? []).map(error => `${error.keyword} ${error.instancePath} ${error.params.missingProperty ?? error.params.additionalProperty ?? ''}`.trim())
  }
  const cellPath = '/cells/text-attachments/claude-code'

  it('accepts a supported cell and a limited cell with a matching audit', () => {
    expect(failures(document())).toEqual([])
    expect(failures(document({}, { support: 'agent-limit', audit: 'unsupported' }))).toEqual([])
    expect(failures(document({}, { support: 'leapmux-limit', audit: 'refusal-covered' }))).toEqual([])
  })

  it.each([
    ['a boolean support value', { support: true }, `enum ${cellPath}/support`],
    ['an unknown support value', { support: 'unsupported' }, `enum ${cellPath}/support`],
  ])('rejects %s', (_name, overrides, expected) => {
    expect(failures(document({}, overrides))).toContain(expected)
  })

  it.each([
    ['supported', { supported: true }],
    ['notes', { notes: '' }],
    ['noteRefs', { noteRefs: [] }],
  ])('rejects the legacy cell field %s', (field, overrides) => {
    expect(failures(document({}, overrides))).toContain(`additionalProperties ${cellPath} ${field}`)
  })

  it('requires the support state and both notes on a cell', () => {
    const { support, userNote, detailNote, ...rest } = cell
    const errors = failures({ ...document(), cells: { 'text-attachments': { 'claude-code': rest } } })
    expect(errors).toEqual(expect.arrayContaining([`required ${cellPath} support`, `required ${cellPath} userNote`, `required ${cellPath} detailNote`]))
  })

  it('rejects a supported cell with a limit audit and a limited cell with a support audit', () => {
    expect(failures(document({}, { audit: 'unsupported' }))).toContain(`enum ${cellPath}/audit`)
    expect(failures(document({}, { support: 'agent-limit', audit: 'covered' }))).toContain(`enum ${cellPath}/audit`)
    expect(failures(document({}, { support: 'leapmux-limit', audit: 'source-verified' }))).toContain(`enum ${cellPath}/audit`)
  })

  it('limits a user note to one paragraph of 400 characters', () => {
    expect(failures(document({}, { userNote: 'x'.repeat(400) }))).toEqual([])
    expect(failures(document({}, { userNote: 'x'.repeat(401) }))).toContain(`maxLength ${cellPath}/userNote`)
    expect(failures(document({}, { userNote: 'one\ntwo' }))).toContain(`pattern ${cellPath}/userNote`)
    expect(failures(document({}, {}, { userNote: 'one\ntwo' }))).toContain('pattern /providerGroups/0/0/userNote')
  })

  it('does not limit a detail note', () => {
    expect(failures(document({}, { detailNote: `${'x'.repeat(5000)}\n\ny` }))).toEqual([])
  })

  it('requires the three support states and rejects the legacy shared notes', () => {
    expect(failures(document({ supportStates: SUPPORT_STATES.slice(0, 2) }))).toContain('minItems /supportStates')
    expect(failures(document({ supportStates: [...SUPPORT_STATES, SUPPORT_STATES[0]] }))).toContain('maxItems /supportStates')
    expect(failures(document({ supportStates: [{ id: 'nope', symbol: 'x', label: 'x' }, SUPPORT_STATES[1], SUPPORT_STATES[2]] }))).toContain('enum /supportStates/0/id')
    const { supportStates, ...withoutStates } = document()
    expect(failures(withoutStates)).toContain('required  supportStates')
    expect(failures(document({ sharedNotes: [{ id: 1, text: 'x' }] }))).toContain('additionalProperties  sharedNotes')
  })

  it('requires both notes on a provider and rejects its legacy note references', () => {
    const { userNote, detailNote, ...bare } = provider
    const errors = failures({ ...document(), providerGroups: [[bare]] })
    expect(errors).toEqual(expect.arrayContaining(['required /providerGroups/0/0 userNote', 'required /providerGroups/0/0 detailNote']))
    expect(failures(document({}, {}, { noteRefs: [1] }))).toContain('additionalProperties /providerGroups/0/0 noteRefs')
    expect(failures(document({}, {}, { notes: '' }))).toContain('additionalProperties /providerGroups/0/0 notes')
  })
})
