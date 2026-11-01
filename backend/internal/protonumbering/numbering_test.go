package protonumbering_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// Imported for their registration in protoregistry.GlobalFiles.
	_ "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	_ "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// payloadOneofStart is the number a payload oneof starts at when a header
// precedes it, as frame.proto, user_ops.proto and worker.proto do on purpose.
const payloadOneofStart = 10

// TestProtoNumberingHasNoHoleAndNoReservation checks every message and enum in
// the leapmux packages against the numbering rule of AGENTS.md.
//
//   - No `reserved` range or name.
//   - Enum values run contiguously from 0.
//   - Message fields run contiguously from 1, or as a contiguous header from 1
//     followed by ONE payload oneof that runs contiguously from 10.
func TestProtoNumberingHasNoHoleAndNoReservation(t *testing.T) {
	var checked int
	protoregistry.GlobalFiles.RangeFilesByPackage("leapmux.v1", func(file protoreflect.FileDescriptor) bool {
		checked += checkFile(t, file)
		return true
	})
	protoregistry.GlobalFiles.RangeFilesByPackage("leapmux.desktop.v1", func(file protoreflect.FileDescriptor) bool {
		checked += checkFile(t, file)
		return true
	})
	require.Positive(t, checked, "the walk found no leapmux message or enum")
}

// checkFile checks each message and enum that file declares, nested ones
// included, and returns how many it checked.
func checkFile(t *testing.T, file protoreflect.FileDescriptor) int {
	t.Helper()
	checked := 0
	for i := range file.Enums().Len() {
		checkEnum(t, file.Enums().Get(i))
		checked++
	}
	for i := range file.Messages().Len() {
		checked += checkMessage(t, file.Messages().Get(i))
	}
	return checked
}

func checkMessage(t *testing.T, message protoreflect.MessageDescriptor) int {
	t.Helper()
	checked := 1
	name := string(message.FullName())
	assert.Zerof(t, message.ReservedRanges().Len(), "%s reserves a field number; renumber the later fields instead", name)
	assert.Zerof(t, message.ReservedNames().Len(), "%s reserves a field name; delete it instead", name)
	if problem := messageNumberingProblem(message); problem != "" {
		t.Errorf("%s: %s", name, problem)
	}
	for i := range message.Enums().Len() {
		checkEnum(t, message.Enums().Get(i))
		checked++
	}
	for i := range message.Messages().Len() {
		nested := message.Messages().Get(i)
		if nested.IsMapEntry() {
			continue
		}
		checked += checkMessage(t, nested)
	}
	return checked
}

func checkEnum(t *testing.T, enum protoreflect.EnumDescriptor) {
	t.Helper()
	name := string(enum.FullName())
	assert.Zerof(t, enum.ReservedRanges().Len(), "%s reserves a value number; renumber the later values instead", name)
	assert.Zerof(t, enum.ReservedNames().Len(), "%s reserves a value name; delete it instead", name)
	numbers := make([]int, 0, enum.Values().Len())
	for i := range enum.Values().Len() {
		numbers = append(numbers, int(enum.Values().Get(i).Number()))
	}
	slices.Sort(numbers)
	assert.Truef(t, isRun(numbers, 0), "%s numbers its values %v; they must run from 0 with no hole", name, numbers)
}

// messageNumberingProblem returns why message breaks the field numbering rule,
// or "" when it follows it.
func messageNumberingProblem(message protoreflect.MessageDescriptor) string {
	fields := message.Fields()
	if fields.Len() == 0 {
		return ""
	}
	var header, payload []int
	var payloadOneof protoreflect.OneofDescriptor
	for i := range fields.Len() {
		field := fields.Get(i)
		number := int(field.Number())
		if number < payloadOneofStart {
			header = append(header, number)
			continue
		}
		// A proto3 `optional` field is a synthetic oneof of one field, so it
		// counts as a header field, not as a payload oneof.
		oneof := field.ContainingOneof()
		if oneof == nil || oneof.IsSynthetic() {
			header = append(header, number)
			continue
		}
		if payloadOneof != nil && payloadOneof != oneof {
			return "two oneofs number fields from 10; one payload oneof may start there"
		}
		payloadOneof = oneof
		payload = append(payload, number)
	}
	slices.Sort(header)
	slices.Sort(payload)
	all := slices.Concat(header, payload)
	if isRun(all, 1) {
		return ""
	}
	if payloadOneof == nil {
		return "fields " + numbersText(all) + " must run from 1 with no hole"
	}
	if len(header) > 0 && !isRun(header, 1) {
		return "header fields " + numbersText(header) + " must run from 1 with no hole"
	}
	if !isRun(payload, payloadOneofStart) {
		return "oneof " + string(payloadOneof.Name()) + " numbers " + numbersText(payload) + "; a payload oneof must run from 10 with no hole"
	}
	// No header field can sit among the payload numbers here. The payload is a
	// run from 10, so it holds 10. A header that runs from 1 and reaches 10
	// would hold 10 too, and a message cannot number two fields alike. A
	// header field past 10 breaks the header run, which the check above
	// refuses.
	return ""
}

// isRun reports whether sorted numbers run start, start+1, ... with no hole.
func isRun(sorted []int, start int) bool {
	for i, number := range sorted {
		if number != start+i {
			return false
		}
	}
	return true
}

func numbersText(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, number := range numbers {
		parts[i] = strconv.Itoa(number)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// TestMessageNumberingProblem checks the rule itself on probe messages, so a
// mistake in it cannot let the whole-registry walk pass every message.
func TestMessageNumberingProblem(t *testing.T) {
	cases := []struct {
		name   string
		plain  []int32
		oneofs [][]int32
		// optional lists proto3 `optional` fields. Each is a synthetic oneof
		// of one field, which the rule counts as a header field.
		optional []int32
		ok       bool
	}{
		{name: "fields from 1", plain: []int32{1, 2, 3}, ok: true},
		{name: "one oneof from 1", oneofs: [][]int32{{1, 2}}, ok: true},
		{name: "a header and a payload oneof from 10", plain: []int32{1, 2}, oneofs: [][]int32{{10, 11, 12}}, ok: true},
		{name: "a payload oneof from 10 alone", oneofs: [][]int32{{10, 11}}, ok: true},
		{name: "plain fields past 9 from 1", plain: []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, ok: true},
		{name: "a hole in the fields", plain: []int32{1, 3}},
		{name: "fields not from 1", plain: []int32{2, 3}},
		{name: "a hole in the header", plain: []int32{1, 3}, oneofs: [][]int32{{10}}},
		{name: "a hole in the payload oneof", plain: []int32{1}, oneofs: [][]int32{{10, 12}}},
		{name: "a payload oneof not from 10", plain: []int32{1}, oneofs: [][]int32{{11, 12}}},
		{name: "a plain field at 10 after a hole", plain: []int32{1, 2, 10}},
		{name: "two oneofs from 10", oneofs: [][]int32{{10}, {11}}},
		{name: "a field before 10 inside a payload oneof", plain: []int32{1}, oneofs: [][]int32{{9, 10, 11}}},
		// A synthetic oneof is not a second payload oneof: the optional field
		// at 10 runs on from the header, and the real oneof follows it.
		{name: "an optional field at 10 before a oneof", plain: []int32{1, 2, 3, 4, 5, 6, 7, 8, 9}, optional: []int32{10}, oneofs: [][]int32{{11, 12}}, ok: true},
		{name: "optional fields from 1", optional: []int32{1, 2}, ok: true},
		{name: "a hole before an optional field", plain: []int32{1}, optional: []int32{3}},
		{name: "an optional field past a payload oneof", plain: []int32{1}, oneofs: [][]int32{{10, 11}}, optional: []int32{13}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := probeMessage(t, fmt.Sprintf("Probe%d", i), tc.plain, tc.oneofs, tc.optional)
			problem := messageNumberingProblem(message)
			if tc.ok {
				assert.Empty(t, problem)
			} else {
				assert.NotEmpty(t, problem, "the rule must refuse this numbering")
			}
		})
	}
}

// probeMessage builds a message with plain string fields at plain, one oneof
// of string fields for each entry of oneofs, and a proto3 `optional` string
// field at each number of optional.
func probeMessage(t *testing.T, name string, plain []int32, oneofs [][]int32, optional []int32) protoreflect.MessageDescriptor {
	t.Helper()
	message := &descriptorpb.DescriptorProto{Name: proto.String(name)}
	addField := func(number int32, oneof *int32, proto3Optional bool) {
		field := &descriptorpb.FieldDescriptorProto{
			Name:       proto.String("f" + strconv.Itoa(int(number))),
			Number:     proto.Int32(number),
			Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:       descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			OneofIndex: oneof,
		}
		if proto3Optional {
			field.Proto3Optional = proto.Bool(true)
		}
		message.Field = append(message.Field, field)
	}
	for _, number := range plain {
		addField(number, nil, false)
	}
	for i, numbers := range oneofs {
		message.OneofDecl = append(message.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("o" + strconv.Itoa(i))})
		for _, number := range numbers {
			addField(number, proto.Int32(int32(i)), false)
		}
	}
	// protodesc requires every synthetic oneof to follow the real ones.
	for _, number := range optional {
		index := int32(len(message.OneofDecl))
		message.OneofDecl = append(message.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("_f" + strconv.Itoa(int(number)))})
		addField(number, proto.Int32(index), true)
	}
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        proto.String(strings.ToLower(name) + ".proto"),
		Package:     proto.String("protonumbering.probe"),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{message},
	}, nil)
	require.NoError(t, err)
	return file.Messages().Get(0)
}
