import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const backend = join(root, 'backend')

describe('go export data', () => {
  it('imports the current compiler output through the analysis dependency', () => {
    const scratch = join(root, '.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'go-export-data-'))
    try {
      // The linter uses this importer. Compile a real standard package to detect format drift.
      const archive = execFileSync('go', ['list', '-export', '-f', '{{.Export}}', 'time'], {
        cwd: backend,
        encoding: 'utf8',
        timeout: 30_000,
      }).trim()
      expect(archive).not.toBe('')
      const probe = join(directory, 'probe.go')
      writeFileSync(probe, `package main

import (
  "fmt"
  "go/token"
  "go/types"
  "os"

  "golang.org/x/tools/go/gcexportdata"
)

func main() {
  archive, err := os.Open(os.Args[1])
  if err != nil { panic(err) }
  defer archive.Close()
  reader, err := gcexportdata.NewReader(archive)
  if err != nil { panic(err) }
  pkg, err := gcexportdata.Read(reader, token.NewFileSet(), make(map[string]*types.Package), "time")
  if err != nil { panic(err) }
  if pkg.Scope().Lookup("Duration") == nil { panic("the imported time package has no Duration type") }
  fmt.Print("time.Duration imported")
}
`)
      const result = spawnSync('go', ['run', probe, archive], {
        cwd: backend,
        encoding: 'utf8',
        timeout: 30_000,
      })
      expect(result.error).toBeUndefined()
      expect(result.status, result.stderr).toBe(0)
      expect(result.stdout).toBe('time.Duration imported')
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  }, 90_000)
})
