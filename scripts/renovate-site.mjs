import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

function requirementFor(path, metadata) {
  return (metadata.Require ?? []).filter(module => path === module.Path || path.startsWith(`${module.Path}/`)).sort((a, b) => b.Path.length - a.Path.length)[0]
}

export function configurationModulePins(configuration, metadata) {
  const imports = configuration.module?.imports ?? []
  if (!Array.isArray(imports))
    throw new Error('Hugo module imports must be an array.')
  return [...new Map(imports.filter(entry => !entry.disable).map((entry) => {
    if (typeof entry.path !== 'string' || entry.path === '')
      throw new Error('A Hugo module import requires a path.')
    const pin = requirementFor(entry.path, metadata)
    if (!pin)
      throw new Error(`Hugo configuration import ${entry.path} has no pinned module requirement.`)
    return [pin.Path, pin]
  })).values()]
}

export function refreshSite({ root, run, read = path => readFileSync(resolve(root, path), 'utf8'), write = (path, content) => writeFileSync(resolve(root, path), content) }) {
  const options = { cwd: 'site', env: { GOWORK: 'off' } }
  const metadata = () => JSON.parse(run('go', ['mod', 'edit', '-json'], { ...options, capture: true }))
  const before = metadata()
  const source = read('site/go.mod')
  const pins = configurationModulePins(Bun.YAML.parse(read('site/hugo.yaml')), before)
  // Hugo's module tidy removes Go tool requirements. Go's tidy preserves tools,
  // but cannot discover modules imported only through Hugo configuration.
  run('go', ['mod', 'tidy'], options)
  const tidied = metadata()
  const missing = pins.filter(pin => !(tidied.Require ?? []).some(module => module.Path === pin.Path))
  if (missing.length) {
    const restored = missing.map((pin) => {
      const pattern = new RegExp(`^[\\t ]*(?:require[\\t ]+)?${RegExp.escape(pin.Path)}[\\t ]+${RegExp.escape(pin.Version)}([\\t ]*//[^\\r\\n]*)?$`, 'm')
      const comment = pattern.exec(source)?.[1] ?? (pin.Indirect ? ' // indirect' : '')
      return `require ${pin.Path} ${pin.Version}${comment}`
    })
    write('site/go.mod', `${read('site/go.mod').trimEnd()}\n\n${restored.join('\n')}\n`)
    run('go', ['mod', 'edit', '-fmt'], options)
  }
  run('go', ['mod', 'download', 'all'], { ...options, env: { GOWORK: 'off', GOFLAGS: '-mod=mod' } })
  const after = metadata()
  for (const tool of before.Tool ?? []) {
    const owner = requirementFor(tool.Path, before)
    if (!(after.Tool ?? []).some(entry => entry.Path === tool.Path) || (owner && !(after.Require ?? []).some(module => module.Path === owner.Path)))
      throw new Error(`Site refresh removed the Go tool requirement for ${tool.Path}.`)
  }
  for (const pin of pins) {
    if (!(after.Require ?? []).some(module => module.Path === pin.Path))
      throw new Error(`Site refresh removed the Hugo configuration import ${pin.Path}.`)
  }
}
