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

    it('pins the support state of every cell of the full grid', () => {
      const { features, checklist, providerContract } = readCodingAgentMatrix()
      expect(validateCodingAgentMatrix(features, checklist, providerContract)).toEqual([])
      const counts = { 'supported': 0, 'agent-limit': 0, 'leapmux-limit': 0 }
      for (const row of Object.values(checklist.cells)) {
        for (const cell of Object.values(row))
          counts[cell.support] += 1
      }
      // A deliberate change of a verdict changes these numbers. Update them with the checklist.
      expect(counts).toEqual({ 'supported': 1082, 'agent-limit': 421, 'leapmux-limit': 87 })
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
  /** Each failure as `keyword path detail`, so a test names the rule that rejects. */
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
  /** Each failure as `keyword path detail`, so a test names the rule that rejects. */
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
