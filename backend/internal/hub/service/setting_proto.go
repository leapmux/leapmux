package service

import (
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// settingDescriptorToProto maps a declared descriptor onto the wire
// descriptor shared by the hub-scope (AdminSettingsService) and
// user-scope (UserService) settings listings.
func settingDescriptorToProto(d settings.Descriptor) *leapmuxv1.SettingDescriptor {
	ui := d.UI()
	out := &leapmuxv1.SettingDescriptor{
		Key:          d.Name(),
		Category:     ui.Category,
		Title:        ui.Title,
		Summary:      ui.Summary,
		HiddenInSolo: ui.HiddenInSolo,
		Restart:      d.Propagation() == settings.PropagationRestart,
	}
	for _, f := range ui.Fields {
		pf := &leapmuxv1.SettingField{
			Name:        f.Name,
			Label:       f.Label,
			Help:        f.Help,
			Kind:        settingFieldKindToProto(f.Kind),
			Unit:        f.Unit,
			Secret:      f.Secret,
			Placeholder: f.Placeholder,
			CustomId:    f.CustomID,
		}
		if f.Min != nil {
			pf.Min = f.Min
		}
		if f.Max != nil {
			pf.Max = f.Max
		}
		if f.MinF != nil {
			pf.MinF = f.MinF
		}
		if f.MaxF != nil {
			pf.MaxF = f.MaxF
		}
		for _, ev := range f.EnumValues {
			pf.EnumValues = append(pf.EnumValues, &leapmuxv1.SettingEnumValue{
				Value: ev.Value,
				Label: ev.Label,
				Help:  ev.Help,
			})
		}
		if f.DependsOn != nil {
			pf.DependsOn = &leapmuxv1.SettingFieldCondition{
				Key:   f.DependsOn.Key,
				Field: f.DependsOn.Field,
				In:    f.DependsOn.In,
			}
		}
		out.Fields = append(out.Fields, pf)
	}
	return out
}

// settingFieldKinds pairs every declared field kind with its wire enum.
// ONE table drives BOTH directions: a second switch written the other way
// round is a copy that drifts, and it already had -- the admin CLI's copy
// printed "boolean" where the Go schema, and the golden account schema
// that pins it, say "bool".
var settingFieldKinds = []struct {
	Kind  settings.FieldKind
	Proto leapmuxv1.SettingFieldKind
}{
	{settings.FieldBool, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_BOOL},
	{settings.FieldInt, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_INT},
	{settings.FieldFloat, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_FLOAT},
	{settings.FieldString, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING},
	{settings.FieldEnum, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_ENUM},
	{settings.FieldStringList, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING_LIST},
	{settings.FieldBytes, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_BYTES},
	{settings.FieldCustom, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_CUSTOM},
}

func settingFieldKindToProto(k settings.FieldKind) leapmuxv1.SettingFieldKind {
	for _, pair := range settingFieldKinds {
		if pair.Kind == k {
			return pair.Proto
		}
	}
	return leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED
}

// SettingFieldKindFromProto resolves a wire enum back to its declared
// kind, reporting false for UNSPECIFIED and for a value this build does
// not know. The admin CLI renders the kind name from it, so the printed
// name is the one FieldKind.String gives.
func SettingFieldKindFromProto(p leapmuxv1.SettingFieldKind) (settings.FieldKind, bool) {
	for _, pair := range settingFieldKinds {
		if pair.Proto == p {
			return pair.Kind, true
		}
	}
	return 0, false
}

// marshalSettingJSON renders a decoded setting value as the wire surface's
// JSON document form. A declared value is always JSON-marshalable; the
// fallback exists so a marshal bug cannot 500 an entire listing.
func marshalSettingJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "\"<undecodable>\""
	}
	return string(b)
}
