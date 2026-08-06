import { describe, expect, it } from 'vitest'

import { installedCopies } from '~/test-support/installedCopies'

// The markdown pipeline in `~/lib/markdownProcessor` threads one hast tree
// through packages that each depend on `@types/hast` under their own range:
// remark-rehype and mdast-util-to-hast build it, @shikijs/rehype annotates it,
// rehype-stringify and hast-util-to-html serialize it.
//
// Those ranges all admit the same version, but a package manager is free to
// leave a second copy nested rather than dedupe -- and shiki 4.4 asking for
// `^3.0.5` while everyone else asked for `^3.0.0` was enough to make it do so.
// The two copies then declare structurally DIFFERENT `Root` types (3.0.5
// replaced the loose `Properties` record with per-attribute types), and `tsc`
// rejects passing one pipeline's tree to the other with a wall of
// "Root is not assignable to Root".
//
// `overrides["@types/hast"]` in package.json collapses them. This guards that
// entry: nothing else fails when it is dropped until two versions happen to
// diverge again, at which point the error names only the symptom.
describe('@types/hast install', () => {
  it('resolves to exactly one copy, so every hast tree is one type', () => {
    const copies = installedCopies('@types/hast')

    expect(
      copies,
      'Multiple @types/hast installs -- the markdown pipeline\'s Root types will not be assignable '
      + 'to each other. Restore the `overrides["@types/hast"]` entry in package.json '
      + `and re-run \`bun install --force\`:\n  ${copies.join('\n  ')}`,
    ).toHaveLength(1)
  })
})
