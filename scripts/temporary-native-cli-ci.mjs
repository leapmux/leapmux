import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'

export function assertHostedRunner(env) {
  if (env.GITHUB_ACTIONS !== 'true' || env.RUNNER_ENVIRONMENT !== 'github-hosted' || !env.GITHUB_RUN_ID || !env.RUNNER_TEMP)
    throw new Error('Native service CI fixtures require a GitHub-hosted disposable runner')
  if (!path.isAbsolute(env.RUNNER_TEMP))
    throw new Error('The disposable runner temporary directory must be absolute')
}

export function nativeCLISelection(hasDesktop) {
  return hasDesktop ? '^TestNativeServiceCLIConfiguration$' : '^TestNativeServiceCLIConfiguration/[^/]+/boot$'
}

const common = `package main
import (
 "fmt"
 "os"
 "os/user"
 "log"
 "github.com/leapmux/leapmux/internal/util/atomicfile"
 "go.yaml.in/yaml/v3"
)
func main() {
 if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("GITHUB_RUN_ID") == "" { log.Fatal("native fixture requires a hosted disposable CI runner") }
 reference := "LeapMux/CI/" + os.Getenv("GITHUB_REPOSITORY") + "/" + os.Getenv("GITHUB_RUN_ID") + "/" + os.Getenv("GITHUB_RUN_ATTEMPT")
 if len(os.Args) == 2 && os.Args[1] == "cleanup" { if err := cleanupCredential(reference); err != nil { log.Fatal(err) }; return }
 if len(os.Args) != 3 || os.Args[1] != "prepare" { log.Fatal("fixture preparation requires an output path") }
 account, err := user.Current(); if err != nil { log.Fatal(err) }
 credential, err := prepareCredential(account, reference); if err != nil { log.Fatal(err) }
 values := map[string]any{"account_id": account.Uid, "account_name": account.Username, "disposable_environment": true, "credential_reference": nil}
 if credential != "" { values["credential_reference"] = credential }
 data, err := yaml.Marshal(values); if err == nil { err = atomicfile.WriteFileDurable(os.Args[2], data, 0600) }
 if err != nil { _ = cleanupCredential(reference); log.Fatal(fmt.Errorf("write private CI account fixture: %w", err)) }
}
`
const unix = `package main
import "os/user"
func prepareCredential(*user.User, string) (string,error) { return "",nil }
func cleanupCredential(string) error { return nil }
`
const windows = `package main
import (
 "crypto/rand"
 "fmt"
 "os/user"
 "runtime"
 "strings"
 "unsafe"
 "golang.org/x/sys/windows"
)
type credential struct {
 Flags, Type uint32
 TargetName, Comment *uint16
 LastWritten windows.Filetime
 BlobSize uint32
 Blob *byte
 Persist, AttributeCount uint32
 Attributes uintptr
 TargetAlias, Username *uint16
}
var credDLL = windows.NewLazySystemDLL("advapi32.dll")
var credWrite = credDLL.NewProc("CredWriteW")
var credDelete = credDLL.NewProc("CredDeleteW")
var netSet = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetUserSetInfo")
func prepareCredential(account *user.User, reference string) (string,error) {
 if !windows.GetCurrentProcessToken().IsElevated() { return "",fmt.Errorf("native service CI requires an elevated hosted runner") }
 local := account.Username[strings.LastIndex(account.Username,"\\\\")+1:]
 sid,_,_,err := windows.LookupSID(".",local)
 if err != nil || sid.String()!=account.Uid { return "",fmt.Errorf("native service CI requires the current local runner account") }
 password := make([]byte,52); defer clear(password)
 if _,err := rand.Read(password[:48]); err != nil { return "",err }
 alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
 for i := range password[:48] { password[i]=alphabet[password[i]&63] }
 copy(password[48:],[]byte("aA1!"))
 encoded := make([]uint16,len(password)+1); defer clear(encoded)
 blob := make([]byte,len(password)*2); defer clear(blob)
 for i,value := range password { encoded[i]=uint16(value); blob[i*2]=value }
 username,err := windows.UTF16PtrFromString(local); if err != nil { return "",err }
 info := struct{Password *uint16}{&encoded[0]}
 status,_,_ := netSet.Call(0,uintptr(unsafe.Pointer(username)),1003,uintptr(unsafe.Pointer(&info)),0)
 runtime.KeepAlive(encoded)
 if status != 0 { return "",fmt.Errorf("prepare disposable runner password: Windows code %d",status) }
 target,err := windows.UTF16PtrFromString(reference); if err != nil { return "",err }
 name,err := windows.UTF16PtrFromString(account.Username); if err != nil { return "",err }
 value := credential{Type:1,TargetName:target,BlobSize:uint32(len(blob)),Blob:&blob[0],Persist:1,Username:name}
 ok,_,nativeErr := credWrite.Call(uintptr(unsafe.Pointer(&value)),0)
 runtime.KeepAlive(value); runtime.KeepAlive(blob)
 if ok==0 { return "",fmt.Errorf("store disposable CI credential: %v",nativeErr) }
 return reference,nil
}
func cleanupCredential(reference string) error {
 target,err := windows.UTF16PtrFromString(reference); if err != nil { return err }
 ok,_,nativeErr := credDelete.Call(uintptr(unsafe.Pointer(target)),1,0)
 if ok==0 && nativeErr!=windows.ERROR_NOT_FOUND { return fmt.Errorf("remove CI credential: %v",nativeErr) }
 return nil
}
`

export function fixtureSources(platform) {
  return {
    'main.go': common,
    [`platform_${platform === 'win32'
      ? 'windows'
      : platform === 'darwin'
        ? 'darwin'
        : 'linux'}.go`]: platform === 'win32'
      ? windows
      : unix,
  }
}

function checked(program, args, options) {
  const result = spawnSync(program, args, { stdio: 'inherit', ...options })
  if (result.error)
    throw result.error
  if (result.status !== 0)
    throw new Error(`${program} exited with ${result.status}`)
}

export function main() {
  assertHostedRunner(process.env)
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
  const scratch = path.join(root, 'backend', '.tmp')
  mkdirSync(scratch, { recursive: true })
  const helper = mkdtempSync(path.join(scratch, 'native-cli-ci-'))
  const fixture = path.join(process.env.RUNNER_TEMP, 'leapmux-native-cli-account.yaml')
  for (const [name, source] of Object.entries(fixtureSources(process.platform)))
    writeFileSync(path.join(helper, name), source, { mode: 0o600 })
  const runHelper = args => checked('go', ['run', '.', ...args], { cwd: helper })
  let hasDesktop = process.platform === 'win32'
  if (process.platform === 'darwin')
    hasDesktop = spawnSync('/bin/launchctl', ['print', `gui/${os.userInfo().uid}`], { stdio: 'ignore' }).status === 0
  if (process.platform === 'linux')
    hasDesktop = spawnSync('/usr/bin/systemctl', ['--user', 'is-active', 'graphical-session.target'], { stdio: 'ignore' }).status === 0
  try {
    runHelper(['prepare', fixture])
    console.log(`Native CLI service E2E selection: ${process.platform}, ${hasDesktop ? 'login and boot' : 'boot'}`)
    checked(process.platform === 'win32'
      ? 'task.exe'
      : 'task', [
      'test-cli-services',
      `SERVICE_ACCOUNT_FILE=${fixture}`,
      '--',
      '-run',
      nativeCLISelection(hasDesktop),
      '-count=1',
      '-timeout=20m',
    ], { cwd: root })
  }
  finally {
    try {
      runHelper(['cleanup'])
    }
    finally {
      rmSync(fixture, { force: true })
      rmSync(helper, { recursive: true, force: true })
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  main()
