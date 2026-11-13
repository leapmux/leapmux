import { render, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { shikiHighlighter } from '~/lib/shikiSyncHighlighter'
import { syntaxPairFor } from '~/lib/syntaxThemes'
import { setSyntaxTheme } from '~/lib/syntaxThemeStore'
import { DEFAULT_THEME_ID } from '~/styles/themes'
import { recordWorkerRequests, settleWorkerReplies } from '~/test-support/fakeWorker'
import { useTestStorage } from '~/test-support/persistentStorage'
import { MarkdownEditor } from './MarkdownEditor'

// The composer's code blocks, coloured by the real Shiki token worker that the
// stand-in Worker runs (see ~/test-support/fakeWorker).

useTestStorage()

const CODE = 'const answer = 42'
const SEED = `\`\`\`typescript\n${CODE}\n\`\`\``

afterEach(async () => {
  // The theme is module state, so put the theme of the file back for the next test.
  await setSyntaxTheme(syntaxPairFor(DEFAULT_THEME_ID), shikiHighlighter)
})

/** Render an editor whose document holds one TypeScript code block, and wait until it is built. */
async function renderEditor() {
  const rendered = render(() => (
    <PreferencesProvider>
      <MarkdownEditor surface="goal" initialMarkdown={SEED} onSend={() => {}} />
    </PreferencesProvider>
  ))
  await waitFor(() => expect(rendered.container.querySelector('.ProseMirror pre')).not.toBeNull())
  return rendered
}

/** The class list of each token span that the highlight plugin draws in the code block. */
function tokenClasses(container: HTMLElement): string[] {
  return [
    ...container.querySelectorAll('.ProseMirror pre .shiki'),
  ].map(span => span.className)
}

describe('MarkdownEditor code highlighting', () => {
  it('paints the code blocks of an editor that mounts again from the tokens of the first, and asks the worker nothing', async () => {
    const first = await renderEditor()
    await settleWorkerReplies()
    const painted = tokenClasses(first.container)
    expect(painted.length).toBeGreaterThan(0)
    first.unmount()

    // The composer mounts again, as after a switch to another tab and back.
    const requests = recordWorkerRequests('lib/shikiWorker.ts')
    const second = await renderEditor()

    expect(tokenClasses(second.container)).toEqual(painted)
    await settleWorkerReplies()
    expect(requests).toEqual([])
  })

  it('colours its code blocks again under a new syntax theme', async () => {
    const { container } = await renderEditor()
    await settleWorkerReplies()
    const before = tokenClasses(container)
    expect(before.length).toBeGreaterThan(0)
    const requests = recordWorkerRequests('lib/shikiWorker.ts')
    const nord = syntaxPairFor('nord')

    await setSyntaxTheme(nord, shikiHighlighter)
    // The block keeps the colours of the old theme while the worker tokenizes.
    expect(tokenClasses(container)).toEqual(before)
    await settleWorkerReplies()

    expect(requests.map(request => ({ code: request.code, syntax: request.syntax }))).toEqual([
      { code: CODE, syntax: nord },
    ])
    const after = tokenClasses(container)
    expect(after.length).toBeGreaterThan(0)
    expect(after).not.toEqual(before)
  })
})
