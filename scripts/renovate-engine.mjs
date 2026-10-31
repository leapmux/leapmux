import { createRequire } from 'node:module'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

// Every module that loads a package from the pinned Renovate install uses this one resolver.
// Keep this module free of top-level await: the CommonJS configuration requires it synchronously.
export const requireRenovate = createRequire(new URL('../.github/renovate/package.json', import.meta.url))

const moduleUrl = relative => pathToFileURL(requireRenovate.resolve(`renovate/dist/${relative}`)).href

let regexEngine
// Bun cannot load the RE2 addon that install-renovate compiles for Node: Bun ends with
// a segmentation fault. Renovate reads RENOVATE_X_IGNORE_RE2 only once, when
// util/regex.js first loads. Under Bun, load that module first with the switch set,
// then restore the environment, so that a child process, such as the Renovate CLI
// under Node, still loads RE2.
function prepareRegexEngine() {
  if (!process.versions.bun)
    return null
  regexEngine ??= (async () => {
    const previous = process.env.RENOVATE_X_IGNORE_RE2
    process.env.RENOVATE_X_IGNORE_RE2 = 'true'
    try {
      await import(moduleUrl('util/regex.js'))
    }
    finally {
      if (previous === undefined)
        delete process.env.RENOVATE_X_IGNORE_RE2
      else
        process.env.RENOVATE_X_IGNORE_RE2 = previous
    }
  })()
  return regexEngine
}

// Renovate exposes no public policy API. Resolve internals from its dedicated pinned manifest.
export async function renovateModule(relative) {
  await prepareRegexEngine()
  return import(moduleUrl(relative))
}

/** Return the Handlebars instance that Renovate's template renderer compiles with. */
export function renovateHandlebars() {
  return createRequire(requireRenovate.resolve('renovate/dist/util/template/index.js'))('handlebars')
}

/**
 * Register a helper on Renovate's Handlebars instance that renders only when Handlebars
 * calls it with the root context of a template and `accepts(root)` holds. Otherwise it
 * throws `rejection`. Return a function that restores the helper that `name` held before.
 */
export function registerRootHelper(name, { accepts, rejection }, render) {
  const handlebars = renovateHandlebars()
  const previous = handlebars.helpers[name]
  handlebars.registerHelper(name, function (options) {
    const root = options.data?.root
    if (this !== root || !accepts(root))
      throw new Error(rejection)
    return render(root)
  })
  return () => {
    if (previous)
      handlebars.registerHelper(name, previous)
    else
      handlebars.unregisterHelper(name)
  }
}
