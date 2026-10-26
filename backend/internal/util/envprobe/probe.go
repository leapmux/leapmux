// Package envprobe reads a native child's environment after its shell profile.
// Both worker executables handle its private command before ordinary CLI dispatch.
package envprobe

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

const Argument = "--leapmux-internal-env-probe"

const responsePrefix = "\x00__LEAPMUX_NATIVE_ENV__\x00"

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Request struct {
	Variables []string `json:"variables"`
	// PathVariables maps an environment variable to the characters its consumer trims.
	// Resolved paths preserve dot segments for the operating system's file lookup.
	PathVariables map[string]string `json:"pathVariables,omitempty"`
}

type Snapshot struct {
	Values     map[string]string `json:"values"`
	WorkingDir string            `json:"workingDir"`
	Paths      map[string]string `json:"paths,omitempty"`
}

func (r Request) Validate() error {
	for _, name := range r.Variables {
		if !variableName.MatchString(name) {
			return fmt.Errorf("environment variable %q is not a plain identifier", name)
		}
	}
	for name := range r.PathVariables {
		if !variableName.MatchString(name) {
			return fmt.Errorf("environment variable %q is not a plain identifier", name)
		}
	}
	return nil
}

// Arguments returns the arguments that start the probe. The request travels as
// JSON in unpadded base64url. A shell passes the request to the probe as a
// native argument, and a shell can rewrite plain JSON on that path:
//   - Windows PowerShell 5.1, and PowerShell 7 in its Legacy argument passing,
//     pass each embedded double quote unescaped. The probe's command-line
//     parser then removes the quote, and the JSON no longer decodes.
//   - Git Bash converts an argument that it reads as a POSIX path.
//
// The base64url alphabet holds only letters, digits, "-" and "_". No shell
// and no command-line parser rewrites these, and no slash makes the argument
// look like a path.
func (r Request) Arguments() ([]string, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return []string{Argument, requestEncoding.EncodeToString(data)}, nil
}

// requestEncoding is the encoding of the request argument (see Arguments).
// Strict decoding requires zero trailing padding bits.
var requestEncoding = base64.RawURLEncoding.Strict()

// Run handles only the private probe command. Values travel through stdout,
// never command arguments or a temporary file. Ordinary CLI arguments pass through.
func Run(args []string, stdout, stderr io.Writer) (exitCode int, handled bool) {
	if len(args) == 0 || args[0] != Argument {
		return 0, false
	}
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "invalid internal environment probe arguments")
		return 2, true
	}
	data, err := requestEncoding.DecodeString(args[1])
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid internal environment probe request")
		return 2, true
	}
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid internal environment probe request")
		return 2, true
	}
	if decoder.Decode(new(any)) != io.EOF {
		_, _ = fmt.Fprintln(stderr, "invalid internal environment probe request")
		return 2, true
	}
	if err := request.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2, true
	}
	snapshot, err := capture(request)
	if err == nil {
		_, err = io.WriteString(stdout, responsePrefix)
	}
	if err == nil {
		err = json.NewEncoder(stdout).Encode(snapshot)
	}
	if err == nil {
		_, err = io.WriteString(stdout, "\x00")
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "the internal environment probe could not complete")
		return 1, true
	}
	return 0, true
}

func capture(request Request) (Snapshot, error) {
	// os.Getwd can return logical PWD on Unix. Native runtimes use the actual
	// process directory, so read it directly from the operating system instead.
	workingDir, err := syscall.Getwd()
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Values: map[string]string{}, WorkingDir: workingDir, Paths: map[string]string{}}
	for _, name := range request.Variables {
		snapshot.Values[name] = os.Getenv(name)
	}
	for name, cutset := range request.PathVariables {
		value := os.Getenv(name)
		snapshot.Values[name] = value
		value = strings.Trim(value, cutset)
		if value == "" {
			snapshot.Paths[name] = ""
			continue
		}
		snapshot.Paths[name] = rootedReadPath(value, workingDir)
	}
	return snapshot, nil
}

// rootedReadPath preserves symlink/../file semantics. Lexical normalization is
// separate: callers can use filepath.Abs for an identity hash without changing the read.
func rootedReadPath(path, workingDir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if runtime.GOOS != "windows" {
		return workingDir + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		if os.IsPathSeparator(path[0]) {
			return filepath.VolumeName(workingDir) + path
		}
		return workingDir + string(filepath.Separator) + path
	}
	if strings.EqualFold(volume, filepath.VolumeName(workingDir)) {
		return workingDir + string(filepath.Separator) + path[len(volume):]
	}
	// Node resolves a drive-relative path from that drive's native environment
	// entry. A drive with no matching entry starts at its root.
	driveDir := os.Getenv("=" + volume)
	if !filepath.IsAbs(driveDir) || !strings.EqualFold(filepath.VolumeName(driveDir), volume) {
		driveDir = volume + string(filepath.Separator)
	}
	return driveDir + string(filepath.Separator) + path[len(volume):]
}

// Decode ignores profile output before the native response. NUL framing cannot
// collide with JSON string contents, which encode NUL as an escape sequence.
func Decode(output []byte) (Snapshot, error) {
	start := bytes.LastIndex(output, []byte(responsePrefix))
	if start < 0 {
		return Snapshot{}, fmt.Errorf("the native environment response is absent")
	}
	data := output[start+len(responsePrefix):]
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return Snapshot{}, fmt.Errorf("the native environment response is incomplete")
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data[:end], &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("the native environment response is invalid")
	}
	if snapshot.Values == nil || !filepath.IsAbs(snapshot.WorkingDir) {
		return Snapshot{}, fmt.Errorf("the native environment response has no process context")
	}
	return snapshot, nil
}
