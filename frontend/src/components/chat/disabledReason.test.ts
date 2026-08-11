import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { DEFAULT_DISABLED_PLACEHOLDER } from './AgentEditorPanel'

// The composer states why it is dead on three surfaces: the hint row above the
// box, the box's own placeholder, and the [+] menu's attach item. They used to
// disagree -- the menu carried its own hardcoded sentence, so a subagent tab
// read "subagent" inside the box and "agent" in the menu.
//
// The fix is ONE resolution, in AgentEditorPanel, handed down as an already-
// resolved string. Asserting the resolution itself would only re-test `||`, so
// this guards the property that actually decays: that no other module grows a
// second default. A leaf that reintroduces `props.x || 'Connection to...'` is
// invisible to a type check and to every render test, because it produces the
// right string until the day the two copies differ.

const chatRoot = dirname(fileURLToPath(import.meta.url))
const frontendSrc = resolve(chatRoot, '..', '..')

/** Modules that legitimately mention the default: the owner and this guard. */
const OWNERS = [
  join(chatRoot, 'AgentEditorPanel.tsx'),
  fileURLToPath(import.meta.url),
]

describe('the disabled-composer reason', () => {
  it('has exactly one default, owned by the composer', () => {
    expect(DEFAULT_DISABLED_PLACEHOLDER).toBe('Connection to the agent was lost.')
  })

  it('is not re-defaulted in any other module', async () => {
    const { globSync } = await import('node:fs')
    const sources = globSync('**/*.{ts,tsx}', { cwd: frontendSrc })
      .map(rel => join(frontendSrc, rel))
      .filter(path => !OWNERS.includes(path))

    const offenders = sources.filter(path =>
      readFileSync(path, 'utf8').includes(DEFAULT_DISABLED_PLACEHOLDER),
    )

    expect(offenders).toEqual([])
  })
})
