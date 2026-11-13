import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'

const source = `package main
import ("archive/tar"; "compress/gzip"; "fmt"; "io"; "os"; "path/filepath"; "strings")
func main() {
 input, err := os.Open(os.Args[1]); if err != nil { panic(err) }; defer input.Close()
 compressed, err := gzip.NewReader(input); if err != nil { panic(err) }; defer compressed.Close()
 destination, err := filepath.Abs(os.Args[2]); if err != nil { panic(err) }
 archive := tar.NewReader(compressed)
 count := 0
 for {
  header, err := archive.Next(); if err == io.EOF { break }; if err != nil { panic(err) }
  if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA { panic("the reference reader accepts regular files only") }
  target := filepath.Join(destination, filepath.FromSlash(header.Name))
  relative, err := filepath.Rel(destination, target)
  if err != nil || filepath.IsAbs(header.Name) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) { panic("unsafe archive path") }
  if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil { panic(err) }
  output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); if err != nil { panic(err) }
  size, copyErr := io.Copy(output, archive); closeErr := output.Close()
  if copyErr != nil { panic(copyErr) }; if closeErr != nil { panic(closeErr) }; if size != header.Size { panic("incorrect file size") }
  count++
 }
 // Read the gzip trailer too, so a correct tar payload cannot hide a corrupt checksum.
 if _, err := io.Copy(io.Discard, compressed); err != nil { panic(err) }
 fmt.Println(count)
}
`

/** Use Go's independent UTF-8 reader: Windows bsdtar skips valid filenames it cannot express in its active code page. */
export function extractTarForTests(archive, destination) {
  const parent = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(parent, { recursive: true })
  const directory = mkdtempSync(join(parent, 'tar-reference-reader-'))
  const program = join(directory, 'reader.go')
  const failures = []
  let count
  try {
    writeFileSync(program, source)
    const env = {
      ...process.env,
      GOENV: 'off',
      GOWORK: 'off',
      GOTOOLCHAIN: 'local',
      GOFLAGS: '-mod=readonly',
    }
    const host = spawnSync('go', [
      'env',
      '-json',
      'GOHOSTOS',
      'GOHOSTARCH',
    ], { env, encoding: 'utf8' })
    if (host.error || host.status !== 0)
      throw new Error(`The reference reader's Go host query failed: ${host.error?.message ?? host.stderr}`)
    const { GOHOSTOS, GOHOSTARCH } = JSON.parse(host.stdout)
    const result = spawnSync('go', ['run', program, archive, destination], {
      env: { ...env, GOOS: GOHOSTOS, GOARCH: GOHOSTARCH },
      encoding: 'utf8',
    })
    if (result.error || result.status !== 0)
      throw new Error(`The reference tar reader failed: ${result.error?.message ?? result.stderr}`)
    count = Number(result.stdout.trim())
    if (!Number.isSafeInteger(count) || count < 0)
      throw new Error('The reference tar reader returned an invalid file count.')
  }
  catch (error) {
    failures.push(error)
  }
  try {
    rmSync(directory, {
      recursive: true,
      force: true,
      maxRetries: 10,
      retryDelay: 100,
    })
  }
  catch (error) {
    failures.push(error)
  }
  if (failures.length === 1)
    throw failures[0]
  if (failures.length)
    throw new AggregateError(failures, 'The reference tar reader and its cleanup failed.')
  return count
}
