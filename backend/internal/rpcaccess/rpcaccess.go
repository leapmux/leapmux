// Package rpcaccess indexes the access options that the leapmux.v1 protos
// declare on every service and method (proto/leapmux/v1/access.proto).
//
// The proto is the one source of what a method demands of a scoped credential
// and where it is served. The hub derives its scope and public-procedure tables
// from this index, and the worker's registrar reads each method's scope and
// reply shape from it, so neither side keeps a hand-written copy that could
// drift from the declaration.
package rpcaccess

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Package is the proto package whose services the index covers.
const Package protoreflect.FullName = "leapmux.v1"

// Method is one declared leapmux.v1 method and its access options.
type Method struct {
	// Procedure is the Connect procedure path: /leapmux.v1.<Service>/<Method>.
	Procedure string
	// Host is the service's ServiceHost option. UNSPECIFIED when the service
	// declares none.
	Host leapmuxv1.ServiceHost
	// Access is the method's MethodAccess option, or nil when the method
	// declares none. Each call to Lookup, Methods or All returns a fresh
	// copy, so a caller may keep or change it without affecting the index.
	Access *leapmuxv1.MethodAccess
	// ServerStreaming reports whether the method returns a stream.
	ServerStreaming bool
}

// index maps a procedure path to its method. Built once at package init from
// the descriptors leapmuxv1 registers; the descriptors never change after init,
// so the index needs no lock.
var index = build()

func build() map[string]Method {
	out := make(map[string]Method)
	protoregistry.GlobalFiles.RangeFilesByPackage(Package, func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := range services.Len() {
			sd := services.Get(i)
			host := leapmuxv1.ServiceHost_SERVICE_HOST_UNSPECIFIED
			if proto.HasExtension(sd.Options(), leapmuxv1.E_Host) {
				host = proto.GetExtension(sd.Options(), leapmuxv1.E_Host).(leapmuxv1.ServiceHost)
			}
			methods := sd.Methods()
			for j := range methods.Len() {
				md := methods.Get(j)
				var access *leapmuxv1.MethodAccess
				if proto.HasExtension(md.Options(), leapmuxv1.E_Access) {
					access = proto.GetExtension(md.Options(), leapmuxv1.E_Access).(*leapmuxv1.MethodAccess)
				}
				procedure := "/" + string(sd.FullName()) + "/" + string(md.Name())
				out[procedure] = Method{
					Procedure:       procedure,
					Host:            host,
					Access:          access,
					ServerStreaming: md.IsStreamingServer(),
				}
			}
		}
		return true
	})
	return out
}

// copyOf returns m with its own copy of Access.
func copyOf(m Method) Method {
	if m.Access != nil {
		m.Access = proto.CloneOf(m.Access)
	}
	return m
}

// Lookup returns the declared method for a procedure path.
func Lookup(procedure string) (Method, bool) {
	m, ok := index[procedure]
	if !ok {
		return Method{}, false
	}
	return copyOf(m), true
}

// Methods returns every declared method of a service with the given host,
// sorted by procedure.
func Methods(host leapmuxv1.ServiceHost) []Method {
	var out []Method
	for _, m := range index {
		if m.Host == host {
			out = append(out, copyOf(m))
		}
	}
	sortByProcedure(out)
	return out
}

// All returns every declared method, sorted by procedure.
func All() []Method {
	out := make([]Method, 0, len(index))
	for _, m := range index {
		out = append(out, copyOf(m))
	}
	sortByProcedure(out)
	return out
}

func sortByProcedure(methods []Method) {
	sort.Slice(methods, func(i, j int) bool { return methods[i].Procedure < methods[j].Procedure })
}

// ConstName returns the name of the leapmuxv1connect constant that holds a
// procedure path: /leapmux.v1.WorkerAgentService/OpenAgent becomes
// WorkerAgentServiceOpenAgentProcedure. It returns "" for a path that is not
// of that form. Source-scanning tests use it to match a registration written
// with the constant against the declared procedure.
func ConstName(procedure string) string {
	rest, ok := strings.CutPrefix(procedure, "/"+string(Package)+".")
	if !ok {
		return ""
	}
	service, method, ok := strings.Cut(rest, "/")
	if !ok || service == "" || method == "" || strings.Contains(method, "/") {
		return ""
	}
	return service + method + "Procedure"
}
