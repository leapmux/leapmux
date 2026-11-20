package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hubrpc"
	"github.com/leapmux/leapmux/internal/rpcaccess"
)

// newRPCCommand builds `rpc`, which calls one procedure of the hub or of a
// Worker by its name.
//
// The proto files are the one source of the procedures: rpcaccess indexes
// every leapmux.v1 method with the host that serves it, which the hub and
// the Worker read for their own tables too, and the descriptors that the
// generated package registers give the request and the response types. A
// procedure that a later version adds is callable with no change here.
func newRPCCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "rpc <procedure> [request]",
		Short:   "Call one procedure of the hub or of a Worker with a request in protobuf JSON",
		Mutates: true,
		Long:    rpcLong(),
		Args: map[string]string{
			"procedure": `The procedure, as "<service>/<method>", such as leapmux.v1.WorkspaceService/ListTabs. The "leapmux.v1." prefix and a leading "/" are optional.`,
			"request":   `The request in protobuf JSON, or "-" to read it from standard input. Without it, the request is {}.`,
		},
		Examples: []cli.Example{
			{Comment: "List the tabs of one workspace.", Command: `leapmux control rpc leapmux.v1.WorkspaceService/ListTabs '{"workspace_ids": ["87a19d5f-4838-4e34-9f0f-56130f0a62b1"]}'`},
			{Comment: "Read one agent from the Worker of this tab.", Command: `leapmux control rpc leapmux.v1.WorkerAgentService/ListAgents '{"tab_ids": ["4cbb3f34-3c1e-4f0e-9a51-2a6a3b1f0d7e"]}' --worker self`},
			{Comment: "Send the request that a file holds, and print the response as one JSON envelope.", Command: "leapmux control rpc WorkerTerminalService/ListAvailableShells - --worker name:build-box --json < request.json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String(workerFlag, "", `the Worker that serves a Worker procedure: an id, a unique id prefix, "name:<name>" or "self"`)
		},
		Complete: completeProcedures,
		Run:      runRPC,
	})
}

// rpcLong returns the help of `rpc`. It lists the hub procedures that the
// socket of a tab relays from hubrpc.Registry, which the relay itself reads,
// so the list cannot drift from the relay.
func rpcLong() string {
	relayed := make([]string, 0, len(hubrpc.Registry))
	for path := range hubrpc.Registry {
		relayed = append(relayed, "  - "+strings.TrimPrefix(path, "/"))
	}
	slices.Sort(relayed)
	return `Call one procedure of the hub or of a Worker, and print its response. Use it
for a call that no other command makes.

The request is protobuf JSON: the second argument, "-" to read it from
standard input, or {} when the command line omits it. The command refuses a
field that the request type does not declare, before it sends anything. The
response prints as protobuf JSON with the field names of the .proto files,
indented. --json prints it as the data of one envelope instead. A field that
holds its default value does not print, as protobuf JSON states.

The command passes the request directly to the Hub or Worker. The receiving
service applies its own authorization and validation. Check what the
procedure does before calling one that changes state.

The procedures are the methods of the services in proto/leapmux/v1. A
procedure of a hub service runs on the hub. A procedure of a Worker service
needs --worker, the Worker that serves the call. A procedure that streams
does not run here. "leapmux control events" prints the events of agents and
terminals, and "leapmux control agent messages --follow" prints the messages
of an agent.

Inside a LeapMux tab, the socket of the tab relays these hub procedures
alone:
` + strings.Join(relayed, "\n") + `

Pass --hub <url> to call another hub procedure with your own login.`
}

// rpcProcedure is one procedure that `rpc` calls.
type rpcProcedure struct {
	// path is the procedure path: /leapmux.v1.<Service>/<Method>.
	path string
	host leapmuxv1.ServiceHost
	// method describes the request and the response types.
	method protoreflect.MethodDescriptor
}

// name is the procedure as the help writes it: the path without its leading
// slash.
func (p rpcProcedure) name() string { return strings.TrimPrefix(p.path, "/") }

// procedurePath reads a procedure as the command line writes it:
// "<service>/<method>", with or without a leading slash and the package of
// the service. ok is false for an input of another form.
func procedurePath(input string) (path string, ok bool) {
	name := strings.TrimPrefix(strings.TrimSpace(input), "/")
	service, method, found := strings.Cut(name, "/")
	if !found || service == "" || method == "" || strings.Contains(method, "/") {
		return "", false
	}
	if !strings.Contains(service, ".") {
		service = string(rpcaccess.Package) + "." + service
	}
	return "/" + service + "/" + method, true
}

// lookupProcedure resolves the procedure that input gives, and refuses one
// that `rpc` cannot call: an unknown one, one of the local socket, and one
// that streams.
func lookupProcedure(input string) (rpcProcedure, error) {
	path, ok := procedurePath(input)
	if !ok {
		return rpcProcedure{}, cli.Usagef("%q is not a procedure", input).
			WithHint("write it as <service>/<method>, such as leapmux.v1.WorkspaceService/ListTabs")
	}
	declared, ok := rpcaccess.Lookup(path)
	if !ok {
		return rpcProcedure{}, unknownProcedure(input, path)
	}
	p := rpcProcedure{path: path, host: declared.Host}
	if p.host != leapmuxv1.ServiceHost_SERVICE_HOST_HUB && p.host != leapmuxv1.ServiceHost_SERVICE_HOST_WORKER {
		return rpcProcedure{}, cli.Usagef("%s belongs to the socket of a LeapMux tab, which carries the calls of the CLI, so the CLI does not call it", p.name()).
			WithHint("call a procedure of a hub service or of a Worker service")
	}
	md, err := methodDescriptor(path)
	if err != nil {
		return rpcProcedure{}, err
	}
	if md.IsStreamingClient() || md.IsStreamingServer() {
		return rpcProcedure{}, cli.Usagef("%s streams, and rpc calls a procedure with one response only", p.name()).
			WithHint(`run "leapmux control events" for the events of agents and terminals, or "leapmux control agent messages --follow" for the messages of an agent`)
	}
	p.method = md
	return p, nil
}

// methodDescriptor returns the descriptor of the method of path, a path that
// rpcaccess declares.
func methodDescriptor(path string) (protoreflect.MethodDescriptor, error) {
	service, method, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, cli.Wrap(cli.CodeInternal, err, "read the descriptor of "+service)
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, cli.Newf(cli.CodeInternal, "%s is not a service", service)
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil, cli.Newf(cli.CodeInternal, "the service %s declares no method %s", service, method)
	}
	return md, nil
}

// callableProcedures lists the name of every procedure that `rpc` calls,
// sorted.
func callableProcedures() []string {
	var names []string
	for _, m := range rpcaccess.All() {
		if m.Host != leapmuxv1.ServiceHost_SERVICE_HOST_HUB && m.Host != leapmuxv1.ServiceHost_SERVICE_HOST_WORKER {
			continue
		}
		md, err := methodDescriptor(m.Procedure)
		if err != nil || md.IsStreamingClient() || md.IsStreamingServer() {
			continue
		}
		names = append(names, strings.TrimPrefix(m.Procedure, "/"))
	}
	return names
}

// maxProcedureSuggestions caps the close names that a refusal lists.
const maxProcedureSuggestions = 5

// unknownProcedure is the refusal of a procedure that no service declares,
// with the declared names closest to it.
func unknownProcedure(input, path string) *cli.Error {
	e := cli.Usagef("unknown procedure %q", input)
	if close := closeProcedures(path); len(close) > 0 {
		e = e.WithHint("did you mean %s?", strings.Join(close, " or "))
	}
	return e.WithHint("the procedures are the methods of the services in proto/leapmux/v1, such as leapmux.v1.WorkspaceService/ListTabs")
}

// closeProcedures lists the callable procedures whose names are closest to
// path, at most maxProcedureSuggestions of them. A name differs from path in
// a few characters, ignoring case, or it gives the method of path under
// another service.
func closeProcedures(path string) []string {
	want := strings.ToLower(strings.TrimPrefix(path, "/"))
	_, wantMethod, _ := strings.Cut(want, "/")
	limit := max(2, len([]rune(want))/6)
	type candidate struct {
		name     string
		distance int
	}
	var found []candidate
	for _, name := range callableProcedures() {
		lower := strings.ToLower(name)
		distance := editDistance(want, lower)
		if _, method, _ := strings.Cut(lower, "/"); method == wantMethod {
			distance = min(distance, 1)
		}
		if distance <= limit {
			found = append(found, candidate{name: name, distance: distance})
		}
	}
	slices.SortStableFunc(found, func(a, b candidate) int { return a.distance - b.distance })
	names := make([]string, 0, min(len(found), maxProcedureSuggestions))
	for _, c := range found[:min(len(found), maxProcedureSuggestions)] {
		names = append(names, c.name)
	}
	return names
}

// editDistance is the Levenshtein distance between a and b: the fewest
// insertions, deletions and substitutions of one rune that turn a into b.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// completeProcedures completes the procedure of `rpc` from the declared
// names.
func completeProcedures(_ *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	if spec.Name != "procedure" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, name := range callableProcedures() {
		if strings.HasPrefix(name, toComplete) {
			out = append(out, name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// newMessage returns an empty message of the generated type that md
// describes.
func newMessage(md protoreflect.MessageDescriptor) (proto.Message, error) {
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.FullName())
	if err != nil {
		return nil, cli.Wrap(cli.CodeInternal, err, "find the type "+string(md.FullName()))
	}
	return mt.New().Interface(), nil
}

// rpcRequest reads the request of p: the argument, stdin for "-", or {}
// without the argument.
func rpcRequest(inv *cli.Invocation, p rpcProcedure) (proto.Message, error) {
	raw := []byte("{}")
	if inv.HasArg("request") {
		raw = []byte(inv.Arg("request"))
		if inv.Arg("request") == "-" {
			var err error
			raw, err = io.ReadAll(inv.Stdin)
			if err != nil {
				return nil, cli.Wrap(cli.CodeInternal, err, "read stdin")
			}
		}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, cli.Usagef("the request is empty").
			WithHint("write {} for a request that sets no field")
	}
	in, err := newMessage(p.method.Input())
	if err != nil {
		return nil, err
	}
	if err := protojson.Unmarshal(raw, in); err != nil {
		return nil, cli.Usagef("the request is not a %s in protobuf JSON: %v", p.method.Input().FullName(), err).
			WithHint("see the message %s in proto/leapmux/v1 for its fields", p.method.Input().Name())
	}
	return in, nil
}

// rpcWorkerInput reads the --worker of a Worker procedure, and refuses it
// for a hub procedure. A Worker procedure needs the flag, because the call
// can change state on that Worker: it never defaults to the Worker of the
// tab that runs the command.
func rpcWorkerInput(inv *cli.Invocation, p rpcProcedure) (string, error) {
	switch {
	case p.host == leapmuxv1.ServiceHost_SERVICE_HOST_HUB && inv.Changed(workerFlag):
		return "", cli.Usagef("%s runs on the hub, so it takes no --worker", p.name())
	case p.host == leapmuxv1.ServiceHost_SERVICE_HOST_HUB:
		return "", nil
	case !inv.Changed(workerFlag):
		return "", cli.Usagef("%s runs on a Worker, so it needs --worker", p.name()).
			WithHint("pass --worker <worker>, or --worker self for the Worker of this tab")
	}
	input := strings.TrimSpace(stringFlag(inv, workerFlag))
	if input == "" {
		return "", cli.Usagef("--worker is empty")
	}
	return input, nil
}

// runRPC calls the procedure with the request, and prints the response.
func runRPC(inv *cli.Invocation) (any, error) {
	p, err := lookupProcedure(inv.Arg("procedure"))
	if err != nil {
		return nil, err
	}
	workerInput, err := rpcWorkerInput(inv, p)
	if err != nil {
		return nil, err
	}
	in, err := rpcRequest(inv, p)
	if err != nil {
		return nil, err
	}
	out, err := newMessage(p.method.Output())
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if p.host == leapmuxv1.ServiceHost_SERVICE_HOST_HUB {
		err = callHubProcedure(s.ctx, s.c, p, in, out)
	} else {
		err = callWorkerProcedure(s, workerInput, p, in, out)
	}
	if err != nil {
		return nil, err
	}
	return rpcResponseOf(out)
}

// callHubProcedure calls the hub procedure p. A client with its own login
// calls any hub procedure. The socket of a tab relays only the procedures of
// hubrpc.Registry, so the command refuses another one before it sends
// anything, instead of reporting the relay's refusal.
func callHubProcedure(ctx context.Context, c *control.Client, p rpcProcedure, in, out proto.Message) error {
	if c.IsWorkerIPC() {
		if _, relayed := hubrpc.Registry[p.path]; !relayed {
			return cli.Usagef("the socket of this LeapMux tab does not relay %s to the hub", p.name()).
				WithHint(`pass --hub <url> to call it with your own login; "leapmux control rpc --help" lists the procedures that the socket relays`)
		}
		return withHubVersion(ctx, c, hubCallLocalIPC(ctx, c, p.path, in, out))
	}
	client := connect.NewClient(
		connecthttp.NewTransport(c.HTTPClient, c.ConnectURL(), connecthttp.WithReadMaxBytes(0)),
		c.AuthInterceptor(),
	)
	return withHubVersion(ctx, c, client.CallUnary(ctx, connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: p.path}, in, out))
}

// callWorkerProcedure resolves the Worker workerInput and calls the Worker
// procedure p on it.
func callWorkerProcedure(s *session, workerInput string, p rpcProcedure, in, out proto.Message) error {
	worker, err := s.resolveOne(resolve.KindWorker, workerInput)
	if err != nil {
		return err
	}
	if err := maybePreflightWorker(s.ctx, s.c, worker.ID); err != nil {
		return err
	}
	return callInnerRPC(s.ctx, s.c, worker.ID, p.path, in, out)
}

// rpcResponse is the response of `rpc` in protobuf JSON. JSON embeds it as
// the data of the envelope, and the text form prints it indented.
type rpcResponse struct {
	// raw is the compact protobuf JSON of the response.
	raw json.RawMessage
}

// rpcResponseOf encodes out. protojson varies its whitespace on purpose, so
// the encoding is compacted once, and the text form indents that.
func rpcResponseOf(out proto.Message) (rpcResponse, error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(out)
	if err != nil {
		return rpcResponse{}, cli.Wrap(cli.CodeInternal, err, "encode the response as protobuf JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return rpcResponse{}, cli.Wrap(cli.CodeInternal, err, "compact the protobuf JSON of the response")
	}
	return rpcResponse{raw: compact.Bytes()}, nil
}

func (r rpcResponse) MarshalJSON() ([]byte, error) { return r.raw, nil }

func (r rpcResponse) RenderText(w io.Writer) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, r.raw, "", "  "); err != nil {
		return fmt.Errorf("indent the response: %w", err)
	}
	indented.WriteByte('\n')
	_, err := w.Write(indented.Bytes())
	return err
}
