// ESLint for the build scripts outside frontend/.
//
// These files are not small: they rewrite tracked source (sync-versions),
// generate the shipped legal artifact (generate-notice), drive codegen
// (sync-generated, build-ico), and render and verify the desktop icons
// (desktop/rust/scripts). Nothing linted them until now -- the only config
// lived in frontend/, and `eslint .` runs with cwd=frontend, so `../scripts` came
// back as "File ignored because outside of base path". That is how an unused
// import sat in the version-rewriting script.
//
// There is no root package.json on purpose: the repo's JS toolchain belongs to
// frontend/. So the shared @antfu config is resolved FROM frontend/ rather than
// duplicated, which also keeps the two on one rule set.

import { createRequire } from 'node:module'

const requireFromFrontend = createRequire(new URL('frontend/', import.meta.url))
const { default: antfu } = await import(requireFromFrontend.resolve('@antfu/eslint-config'))

export default antfu({
  stylistic: {
    indent: 2,
    quotes: 'single',
  },
  // Only the scripts outside frontend/. frontend/ has its own config with the
  // Solid and Playwright rules these files have no use for, and it already
  // covers frontend/scripts/. The intermediate directories need a negation each,
  // because `**/*` ignores `desktop/` itself and ESLint cannot re-include a path
  // below an ignored directory.
  ignores: ['**/*', '!scripts/**', '!desktop', '!desktop/rust', '!desktop/rust/scripts/**'],
}, {
  files: ['scripts/**/*.mjs', 'desktop/rust/scripts/**/*.mjs'],
  rules: {
    // These run under `bun scripts/...`, so stdout/stderr IS the interface.
    'no-console': 'off',
  },
})
