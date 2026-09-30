import { describe, expect, it } from 'bun:test'
import { verifyCodingAgentSite } from './verify-coding-agent-site.mjs'

const features = {
  features: [{ id: 'mcp-input-request', label: 'MCP input request', description: 'Collects requested fields.' }],
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

describe('verifyCodingAgentSite', () => {
  it('accepts the generated table, definitions, and full note text', () => {
    expect(verifyCodingAgentSite(html, features, checklist)).toEqual([])
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
