import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { postUpgrade } from './renovate-post-upgrade.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})

function write(root, file, content) {
  mkdirSync(dirname(resolve(root, file)), { recursive: true })
  writeFileSync(resolve(root, file), content)
}

const zipper = String.raw`package main
import ("archive/zip"; "os"; "path/filepath"; "strings"; "encoding/json")
func main() {
 root:=os.Args[1]
 var modules map[string]map[string]string
 if err:=json.Unmarshal([]byte(os.Args[2]),&modules);err!=nil {panic(err)}
 for identity,files:=range modules {
  parts:=strings.Split(identity,"@")
  directory:=filepath.Join(root,parts[0],"@v")
  if err:=os.MkdirAll(directory,0755);err!=nil {panic(err)}
  if err:=os.WriteFile(filepath.Join(directory,parts[1]+".mod"),[]byte(files["go.mod"]),0644);err!=nil {panic(err)}
  info,err:=json.Marshal(map[string]string{"Version":parts[1],"Time":"2026-10-20T00:00:00Z"});if err!=nil {panic(err)}
  if err:=os.WriteFile(filepath.Join(directory,parts[1]+".info"),info,0644);err!=nil {panic(err)}
  if err:=os.WriteFile(filepath.Join(directory,"list"),[]byte(parts[1]+"\n"),0644);err!=nil {panic(err)}
  file,err:=os.Create(filepath.Join(directory,parts[1]+".zip"));if err!=nil {panic(err)}
  archive:=zip.NewWriter(file)
  for name,content:=range files { entry,err:=archive.Create(identity+"/"+name);if err!=nil {panic(err)};if _,err=entry.Write([]byte(content));err!=nil {panic(err)} }
  if err:=archive.Close();err!=nil {panic(err)};if err:=file.Close();err!=nil {panic(err)}
 }
}`

function proxyFixture(modules) {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const fixture = mkdtempSync(resolve(root, '.tmp/renovate-go-integration-'))
  fixtures.push(fixture)
  const env = {
    ...process.env,
    GOTOOLCHAIN: 'local',
    GOWORK: 'off',
    GOPROXY: pathToFileURL(resolve(fixture, 'proxy')).href,
    GOSUMDB: 'off',
    GOMODCACHE: resolve(fixture, 'module-cache'),
  }
  const execute = (command, args, options = {}) => {
    const result = spawnSync(command, args, {
      cwd: resolve(fixture, options.cwd ?? '.'),
      encoding: 'utf8',
      input: options.input,
      env: {
        ...env,
        ...(options.env ?? {}),
        GOFLAGS: `-modcacherw ${options.env?.GOFLAGS ?? ''}`,
      },
    })
    expect(result.status, `${command} ${args.join(' ')}\n${result.stdout}\n${result.stderr}`).toBe(0)
    return result.stdout
  }
  write(fixture, 'zipper.go', zipper)
  execute('go', [
    'run',
    'zipper.go',
    resolve(fixture, 'proxy'),
    JSON.stringify(modules),
  ])
  return { fixture, execute }
}

describe('postUpgrade with real Go metadata and a local module proxy', () => {
  it('downloads moved transitive checksums before the updated tool prepares absent generated packages', async () => {
    const modules = {
      'example.com/tools/sqlc@v1.1.0': {
        'go.mod': 'module example.com/tools/sqlc\ngo 1.27.1\nrequire example.com/moved/v2 v2.0.0\n',
        'cmd/sqlc/main.go': 'package main\nimport ("os"; moved "example.com/moved/v2")\nfunc main() { if moved.Value != 2 { panic("wrong transitive module") }; if err:=os.MkdirAll("generated",0755);err!=nil {panic(err)}; if err:=os.WriteFile("generated/generated.go",[]byte("package generated\\nconst Value = 2\\n"),0644);err!=nil {panic(err)} }\n',
      },
      'example.com/moved/v2@v2.0.0': { 'go.mod': 'module example.com/moved/v2\ngo 1.27.1\n', 'moved.go': 'package moved\nconst Value=2\n' },
    }
    const { fixture, execute } = proxyFixture(modules)
    const replace = '\nreplace google.golang.org/genproto => google.golang.org/genproto v0.0.0-20260420184626-e10c466a9529\n'
    write(fixture, 'backend/go.mod', `module github.com/leapmux/leapmux\ngo 1.27.1\ntool example.com/tools/sqlc/cmd/sqlc\nrequire example.com/tools/sqlc v1.1.0\n${replace}`)
    write(fixture, 'backend/main.go', 'package backend\nimport ("github.com/leapmux/leapmux/generated"; "github.com/leapmux/leapmux/internal/hub/generated/frontend")\nvar Value = generated.Value\nvar Assets = frontend.Assets\n')
    write(fixture, 'backend/main_test.go', 'package backend\nimport ("testing"; "strings")\nfunc TestEmbeddedAssets(t *testing.T) { data,err:=Assets.ReadFile("public/index.html"); if err!=nil || !strings.Contains(string(data),"fixture application") { t.Fatalf("embedded assets are missing: %v %s",err,data) } }\n')
    write(fixture, 'desktop/go/go.mod', `module example.com/desktop\ngo 1.27.1\nrequire github.com/leapmux/leapmux v0.0.0\nreplace github.com/leapmux/leapmux => ../../backend\n${replace}`)
    write(fixture, 'desktop/go/main.go', 'package desktop\nimport backend "github.com/leapmux/leapmux"\nvar Value = backend.Value\n')
    write(fixture, 'go.work', 'go 1.27.1\nuse (\n ./backend\n ./desktop/go\n)\n')
    execute('git', ['init', '--initial-branch=main'])
    for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false' })) execute('git', ['config', key, value])
    execute('git', ['add', 'backend', 'desktop', 'go.work'])
    let generated = false
    const run = (command, args, options = {}) => {
      if (options.cwd === 'site')
        throw new Error('A workspace update invoked the independent site graph.')
      if (command === 'task') {
        if (args[0] === 'prepare-backend') {
          expect(existsSync(resolve(fixture, 'backend/generated'))).toBe(false)
          expect(existsSync(resolve(fixture, 'backend/internal/hub/generated/frontend/public'))).toBe(false)
          const sum = readFileSync(resolve(fixture, 'backend/go.sum'), 'utf8')
          expect(sum).toContain('example.com/tools/sqlc v1.1.0 h1:')
          expect(sum).toContain('example.com/moved/v2 v2.0.0 h1:')
          execute('go', ['tool', 'sqlc'], { cwd: 'backend' })
          write(fixture, 'backend/internal/hub/generated/frontend/embed.go', 'package frontend\nimport "embed"\n//go:embed public/*\nvar Assets embed.FS\n')
          write(fixture, 'backend/internal/hub/generated/frontend/public/index.html', '<!doctype html><p>fixture application</p>')
          generated = true
        }
        return ''
      }
      if (command === 'go' && args[0] === 'work') {
        return execute(command, args, {
          ...options,
          env: {
            GOWORK: resolve(fixture, 'go.work'),
          },
        })
      }
      if (command === 'go' && args[1] === 'tidy')
        expect(generated).toBe(true)
      if (command === 'git')
        return 'backend/go.mod\0'
      return execute(command, args, options)
    }
    await postUpgrade([
      {
        manager: 'gomod',
        packageFile: 'backend/go.mod',
        depName: 'example.com/tools/sqlc',
        depType: 'require',
        newValue: 'v1.1.0',
      },
    ], {
      root: fixture,
      trusted: root,
      run,
      reconcile: async () => {},
    })
    expect(readFileSync(resolve(fixture, 'backend/generated/generated.go'), 'utf8')).toContain('const Value = 2')
    expect(readFileSync(resolve(fixture, 'desktop/go/go.mod'), 'utf8')).toContain('replace github.com/leapmux/leapmux => ../../backend')
    execute('go', ['test', './...'], { cwd: 'backend' })
  })

  it('migrates a configuration-only site module through real metadata and tidy without a workspace', async () => {
    const { fixture, execute } = proxyFixture({
      'example.com/theme/v2@v2.0.0': { 'go.mod': 'module example.com/theme/v2\ngo 1.27.1\n', 'theme.go': 'package theme\n' },
      'example.com/tools/hugo@v1.1.0': { 'go.mod': 'module example.com/tools/hugo\ngo 1.27.1\n', 'cmd/hugo/main.go': 'package main\nimport "fmt"\nfunc main() { fmt.Println("fixture Hugo") }\n' },
    })
    write(fixture, 'site/go.mod', 'module example.com/site\ngo 1.27.1\ntool example.com/tools/hugo/cmd/hugo\nrequire example.com/tools/hugo v1.1.0\nrequire example.com/theme/v2 v2.0.0 // retain configuration import\n')
    write(fixture, 'site/hugo.yaml', '# retain configuration comments\nmodule:\n  imports:\n    - path: example.com/theme # retain\n')
    execute('git', ['init', '--initial-branch=main'])
    for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false' }))
      execute('git', ['config', key, value])
    execute('git', ['add', 'site'])
    await postUpgrade([
      {
        manager: 'gomod',
        packageFile: 'site/go.mod',
        depName: 'example.com/theme',
        depType: 'require',
        newValue: 'v2.0.0',
      },
    ], {
      root: fixture,
      trusted: root,
      run: (command, args, options = {}) => {
        if (command === 'task' || args[0] === 'work' || (command === 'go' && options.cwd !== 'site'))
          throw new Error('A site update invoked workspace processing.')
        return command === 'git'
          ? 'site/go.mod\0site/go.sum\0site/hugo.yaml\0'
          : execute(command, args, options)
      },
    })
    const module = readFileSync(resolve(fixture, 'site/go.mod'), 'utf8')
    expect(module).toContain('example.com/theme/v2 v2.0.0 // retain configuration import')
    expect(module).toContain('example.com/tools/hugo v1.1.0')
    expect(readFileSync(resolve(fixture, 'site/hugo.yaml'), 'utf8')).toBe('# retain configuration comments\nmodule:\n  imports:\n    - path: example.com/theme/v2 # retain\n')
    expect(execute('go', ['tool', 'hugo', '--version'], { cwd: 'site' })).toContain('fixture Hugo')
  })

  it('reports an incompatible Go requirement without downloading another toolchain', async () => {
    const fixture = mkdtempSync(resolve(root, '.tmp/renovate-go-requirement-'))
    fixtures.push(fixture)
    const replacement = '\nreplace google.golang.org/genproto => google.golang.org/genproto v0.0.0-20260420184626-e10c466a9529\n'
    write(fixture, 'backend/go.mod', `module example.com/backend\ngo 1.99.0\n${replacement}`)
    write(fixture, 'desktop/go/go.mod', `module example.com/desktop\ngo 1.27.1\n${replacement}`)
    const git = args => spawnSync('git', args, { cwd: fixture, encoding: 'utf8' })
    expect(git(['init', '--initial-branch=main']).status).toBe(0)
    for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false' }))
      expect(git(['config', key, value]).status).toBe(0)
    expect(git(['add', 'backend', 'desktop']).status).toBe(0)
    await expect(postUpgrade([
      {
        manager: 'gomod',
        packageFile: 'backend/go.mod',
        depName: 'example.com/library',
        depType: 'require',
        newValue: 'v1.0.0',
      },
    ], { root: fixture, trusted: root })).rejects.toThrow('GOTOOLCHAIN=local')
  })
})
