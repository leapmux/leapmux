import { describe, expect, it } from 'bun:test'
import { codingAgentMatrixDimensions, verifyCodingAgentProviderCount, verifyCodingAgentSite } from './verify-coding-agent-site.mjs'

const features = {
  features: [{ id: 'mcp-input-request', label: 'MCP input request', description: 'Collects requested fields.', showInMatrix: true }],
}
const checklist = {
  providerGroups: [[{
    id: 'pi',
    label: 'Pi',
    url: 'https://pi.dev/',
    icon: '/icons/agents/pi.svg',
    notes: 'Provider note.',
    noteRefs: [1],
  }]],
  sharedNotes: [{ id: 1, text: 'Pi extensions:\n- [Plan mode](https://pi.dev/plan).' }],
  cells: {
    'mcp-input-request': {
      pi: { supported: true, noteRefs: [1], notes: 'Cell-specific **value**.' },
    },
  },
}
const html = `
<p>LeapMux supports <span data-agent-provider-count>1</span> providers.</p>
<div class="provider-logos"><a href="https://pi.dev/">
  <img src="/icons/agents/pi.svg" alt="Pi"><span class="provider-hover-label">Pi</span>
</a></div>
<div class="feature-matrix"><table aria-label="Coding agent feature matrix, group 1">
  <thead><tr><th>Feature</th><th><a href="https://pi.dev/">
    <img src="/icons/agents/pi.svg" alt="Pi"><span class="provider-hover-label">Pi</span>
  </a><a href="#note-1">1</a><a href="#note-provider-pi">†</a></th></tr></thead>
  <tbody><tr><th scope="row"><a href="#feature-mcp-input-request">MCP input request</a></th>
    <td>✅<a href="#note-1">1</a><a href="#note-pi-mcp-input-request">†</a></td></tr></tbody>
</table></div>
<section class="feature-matrix-definitions"><dl>
  <dt id="feature-mcp-input-request">MCP input request</dt><dd>Collects requested fields.</dd>
</dl></section>
<section class="feature-matrix-notes"><ol><li id="note-1"><p>Pi extensions:</p><ul><li><a href="https://pi.dev/plan">Plan mode</a>.</li></ul></li></ol>
  <div id="note-provider-pi"><strong>Pi:</strong><p>Provider note.</p></div>
  <div id="note-pi-mcp-input-request"><strong>Pi — MCP input request:</strong><p>Cell-specific <strong>value</strong>.</p></div>
</section>
`

function withHiddenFeature() {
  const extendedFeatures = structuredClone(features)
  extendedFeatures.features.push({ id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and returns an answer.', showInMatrix: false })
  const extendedChecklist = structuredClone(checklist)
  extendedChecklist.sharedNotes.push({ id: 2, text: 'A test-only native limit.' })
  extendedChecklist.cells['basic-chat'] = { pi: { supported: true, noteRefs: [2], notes: 'A test-only cell note.' } }
  return { features: extendedFeatures, checklist: extendedChecklist }
}

describe('verifyCodingAgentSite', () => {
  it('accepts the generated table, definitions, and full note text', () => {
    expect(verifyCodingAgentSite(html, features, checklist)).toEqual([])
  })

  it('rejects a missing provider count', () => {
    const invalid = html.replace('<span data-agent-provider-count>1</span>', 'one')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('the page contains no generated provider count')
  })

  it.each(['0', '2', '26', '', '01', '1 provider'])('rejects an incorrect provider count: %s', (count) => {
    const invalid = html.replace('data-agent-provider-count>1', `data-agent-provider-count>${count}`)
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain(`provider count 1 is ${JSON.stringify(count)}, expected 1`)
  })

  it('checks every provider count on the page', () => {
    const invalid = `${html}<span data-agent-provider-count>26</span>`
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('provider count 2 is "26", expected 1')
  })

  it('accepts multiple provider counts from the same source', () => {
    const valid = `${html}<span data-agent-provider-count>1</span>`
    expect(verifyCodingAgentSite(valid, features, checklist)).toEqual([])
  })

  it('preserves code examples with angle brackets and command-line flags', () => {
    const expected = structuredClone(checklist)
    expected.cells['mcp-input-request'].pi.notes = 'Call `tools.<name>(arguments)` with `--no-extensions`.'
    const valid = html.replace('Cell-specific <strong>value</strong>.', 'Call <code>tools.&lt;name&gt;(arguments)</code> with <code>--no-extensions</code>.')
    expect(verifyCodingAgentSite(valid, features, expected)).toEqual([])
    const missing = valid.replace('tools.&lt;name&gt;(arguments)', 'tools.(arguments)')
    expect(verifyCodingAgentSite(missing, features, expected)).toContain('cell note for pi/mcp-input-request differs from source')
  })

  it('omits test-only rows, definitions, and their notes from the rendered count', () => {
    const extended = withHiddenFeature()
    expect(verifyCodingAgentSite(html, extended.features, extended.checklist)).toEqual([])
    expect(codingAgentMatrixDimensions(extended.features, extended.checklist)).toEqual({ features: 1, cells: 1 })
  })

  it('rejects a test-only row in the published table', () => {
    const extended = withHiddenFeature()
    const invalid = html.replace('</tbody>', '<tr><th scope="row"><a href="#feature-basic-chat">Basic chat</a></th><td>✅</td></tr></tbody>')
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('table 1 has 2 feature rows, expected 1')
  })

  it('rejects a test-only feature definition on the website', () => {
    const extended = withHiddenFeature()
    const invalid = html.replace('</dl>', '<dt id="feature-basic-chat">Basic chat</dt><dd>Receives a prompt and returns an answer.</dd></dl>')
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('hidden feature basic-chat appears in feature definitions')
  })

  it('rejects a shared note used only by test-only cells', () => {
    const extended = withHiddenFeature()
    const invalid = html.replace('</ol>', '<li id="note-2">A test-only native limit.</li></ol>')
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('hidden-only shared note 2 appears on the website')
  })

  it('keeps a shared note that a published provider header uses', () => {
    const extended = withHiddenFeature()
    extended.checklist.providerGroups[0][0].noteRefs.push(2)
    const valid = html.replace('</th></tr></thead>', '<a href="#note-2">2</a></th></tr></thead>')
      .replace('</ol>', '<li id="note-2">A test-only native limit.</li></ol>')
    expect(verifyCodingAgentSite(valid, extended.features, extended.checklist)).toEqual([])
  })

  it('counts shared notes without counting a nested ordered list', () => {
    const extended = withHiddenFeature()
    extended.checklist.providerGroups[0][0].noteRefs.push(2)
    extended.checklist.sharedNotes[1].text = 'Steps:\n1. Open.\n2. Submit.'
    const valid = html.replace('</th></tr></thead>', '<a href="#note-2">2</a></th></tr></thead>')
      .replace('</ol>', '<li id="note-2"><p>Steps:</p><ol><li>Open.</li><li>Submit.</li></ol></li></ol>')
    expect(verifyCodingAgentSite(valid, extended.features, extended.checklist)).toEqual([])
  })

  it('keeps the Markdown list kind in a shared note', () => {
    const invalid = html.replace('<ul>', '<ol>').replace('</ul>', '</ol>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('shared note 1 differs from source')
  })

  it('rejects a test-only cell note on the website', () => {
    const extended = withHiddenFeature()
    const invalid = `${html}<div id="note-pi-basic-chat">A test-only cell note.</div>`
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('hidden cell note for pi/basic-chat appears on the website')
  })

  it('rejects a changed shared note', () => {
    expect(verifyCodingAgentSite(html.replace('Plan mode</a>', 'Plan</a>'), features, checklist))
      .toContain('shared note 1 differs from source')
  })

  it('rejects a changed evidence URL with the same visible text', () => {
    expect(verifyCodingAgentSite(html.replace('https://pi.dev/plan', 'https://pi.dev/wrong'), features, checklist))
      .toContain('shared note 1 differs from source')
  })

  it('rejects a changed provider note', () => {
    expect(verifyCodingAgentSite(html.replace('<p>Provider note.</p>', '<p>Provider.</p>'), features, checklist))
      .toContain('provider note for pi differs from source')
  })

  it('rejects a changed cell note', () => {
    expect(verifyCodingAgentSite(html.replace('<strong>value</strong>', '<strong>other</strong>'), features, checklist))
      .toContain('cell note for pi/mcp-input-request differs from source')
  })

  it('rejects a missing hover label', () => {
    expect(verifyCodingAgentSite(html.replace('<span class="provider-hover-label">Pi</span>', ''), features, checklist))
      .toContain('provider logo 1 has no hover label')
  })

  it('rejects a feature cell without a scoped row header', () => {
    const invalid = html.replace(
      '<th scope="row"><a href="#feature-mcp-input-request">MCP input request</a></th>',
      '<td><a href="#feature-mcp-input-request">MCP input request</a></td>',
    )
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has no row header for mcp-input-request')
  })

  it('rejects a matrix table without an accessible name', () => {
    const invalid = html.replace(' aria-label="Coding agent feature matrix, group 1"', '')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has no accessible name')
  })
})

describe('verifyCodingAgentProviderCount', () => {
  it('accepts a count without a feature table', () => {
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>1</span>', checklist)).toEqual([])
  })

  it('counts providers from every source group', () => {
    const extended = { providerGroups: [[{ id: 'a' }], [{ id: 'b' }, { id: 'c' }]] }
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>3</span>', extended)).toEqual([])
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>1</span>', extended))
      .toContain('provider count 1 is "1", expected 3')
  })

  it('rejects a page without the generated count', () => {
    expect(verifyCodingAgentProviderCount('<p>Three providers.</p>', checklist))
      .toContain('the page contains no generated provider count')
  })
})
