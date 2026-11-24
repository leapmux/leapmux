package cmd

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

func newSettingsCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("settings", "Read and change your account preferences on the hub",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your account settings with their effective values",
			Examples: []cli.Example{
				{Command: "leapmux settings list"},
			},
			Run: runSettingsList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get <key>",
			Short: "Show one account setting, its value, and its schema",
			Examples: []cli.Example{
				{Command: "leapmux settings get appearance"},
			},
			Complete: completeUserSettingsKeys,
			Run:      runSettingsGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set <key> <value>",
			Short:   "Update an account setting from JSON or a bare value",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux settings set appearance '{"theme":"dark"}'`},
			},
			Complete: completeUserSettingsKeys,
			Run:      runSettingsSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "reset <key>",
			Short:       "Reset an account setting back to its default value",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux settings reset appearance"},
			},
			Complete: completeUserSettingsKeys,
			Run:      runSettingsReset,
		}),
	), "pref", "prefs")
}

type userSettingRow struct {
	Key           string          `json:"key" table:"KEY"`
	Customized    bool            `json:"customized" table:"CUSTOMIZED"`
	EffectiveJSON json.RawMessage `json:"effective_json" table:"EFFECTIVE"`
	ValueJSON     json.RawMessage `json:"value_json,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	Description   string          `json:"description,omitempty"`
	ValueSchema   string          `json:"value_schema,omitempty"`
}

func (r userSettingRow) QuietLines() []string { return []string{r.Key} }

type userSettingsListResult struct {
	Settings []userSettingRow `json:"settings"`
}

func (r userSettingsListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Settings))
	for _, s := range r.Settings {
		out = append(out, s.Key)
	}
	return out
}

func userSettingRowOf(v *leapmuxv1.SettingValue, d *leapmuxv1.SettingDescriptor) userSettingRow {
	row := userSettingRow{
		Key:           v.GetKey(),
		Customized:    v.GetCustomized(),
		EffectiveJSON: settingJSONValue(v.GetEffectiveJson()),
		UpdatedAt:     formatTime(v.GetUpdatedAt()),
	}
	if stored := v.GetValueJson(); stored != "" {
		row.ValueJSON = settingJSONValue(stored)
	}
	if d != nil {
		row.Description = d.GetSummary()
		row.ValueSchema = settingShapeFromDescriptor(d)
	}
	return row
}

func runSettingsList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.ListUserSettingsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.UserServiceListUserSettingsProcedure, &leapmuxv1.ListUserSettingsRequest{}, &resp); err != nil {
		return nil, err
	}
	byDescr := map[string]*leapmuxv1.SettingDescriptor{}
	for _, d := range resp.GetDescriptors() {
		byDescr[d.GetKey()] = d
	}
	rows := make([]userSettingRow, 0, len(resp.GetValues()))
	for _, v := range resp.GetValues() {
		rows = append(rows, userSettingRowOf(v, byDescr[v.GetKey()]))
	}
	return userSettingsListResult{Settings: rows}, nil
}

func runSettingsGet(inv *cli.Invocation) (any, error) {
	key := strings.TrimSpace(inv.Arg("key"))
	if key == "" {
		return nil, cli.Usagef("<key> is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.ListUserSettingsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.UserServiceListUserSettingsProcedure, &leapmuxv1.ListUserSettingsRequest{}, &resp); err != nil {
		return nil, err
	}
	var descr *leapmuxv1.SettingDescriptor
	for _, d := range resp.GetDescriptors() {
		if d.GetKey() == key {
			descr = d
			break
		}
	}
	for _, v := range resp.GetValues() {
		if v.GetKey() == key {
			return userSettingRowOf(v, descr), nil
		}
	}
	return nil, cli.Newf(cli.CodeNotFound, "unknown user setting key %q", key).
		WithHint(`list available settings with "leapmux settings list"`)
}

func runSettingsSet(inv *cli.Invocation) (any, error) {
	key := strings.TrimSpace(inv.Arg("key"))
	if key == "" {
		return nil, cli.Usagef("<key> is empty")
	}
	value, err := parseSettingValue(inv.Arg("value"))
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.UpdateUserSettingResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.UserServiceUpdateUserSettingProcedure, &leapmuxv1.UpdateUserSettingRequest{
		Key:         key,
		PartialJson: string(value),
	}, &resp); err != nil {
		return nil, err
	}
	return userSettingRowOf(resp.GetValue(), nil), nil
}

func runSettingsReset(inv *cli.Invocation) (any, error) {
	key := strings.TrimSpace(inv.Arg("key"))
	if key == "" {
		return nil, cli.Usagef("<key> is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.ResetUserSettingResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.UserServiceResetUserSettingProcedure, &leapmuxv1.ResetUserSettingRequest{
		Key: key,
	}, &resp); err != nil {
		return nil, err
	}
	return userSettingRowOf(resp.GetValue(), nil), nil
}

func completeUserSettingsKeys(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	if spec.Name != "key" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	var resp leapmuxv1.ListUserSettingsResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.UserServiceListUserSettingsProcedure, &leapmuxv1.ListUserSettingsRequest{}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, d := range resp.GetDescriptors() {
		if strings.HasPrefix(d.GetKey(), toComplete) {
			out = append(out, d.GetKey()+"\t"+d.GetSummary())
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
