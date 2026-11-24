package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func newAdminNetworkStatus() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "status",
		Short:            "Show the Hub's live TCP listeners, local IPC endpoints, and machine interfaces",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin network status"},
			{Command: "leapmux admin network status --json"},
		},
		Run: runAdminNetworkStatus,
	})
}

type adminBoundAddressRow struct {
	Address string `json:"address" table:"ADDRESS"`
	Source  string `json:"source" table:"SOURCE"`
	Error   string `json:"error,omitempty" table:"ERROR"`
}

type adminNetworkInterfaceAddr struct {
	IP       string `json:"ip"`
	IPv6     bool   `json:"ipv6,omitempty"`
	Loopback bool   `json:"loopback,omitempty"`
}

type adminNetworkInterfaceRow struct {
	Name      string                      `json:"name"`
	Up        bool                        `json:"up"`
	Addresses []adminNetworkInterfaceAddr `json:"addresses,omitempty"`
}

type adminNetworkStatusResult struct {
	BaseTCPAddresses  []string                   `json:"base_tcp_addresses,omitempty"`
	ConfiguredExtra   []string                   `json:"configured,omitempty"`
	LocalIPCAddresses []string                   `json:"local_ipc_addresses,omitempty"`
	Bound             []adminBoundAddressRow     `json:"bound,omitempty"`
	Interfaces        []adminNetworkInterfaceRow `json:"interfaces,omitempty"`
}

func (r adminNetworkStatusResult) RenderText(w io.Writer) error {
	return r.RenderTextWithOptions(w, cli.TableOptions{})
}

func (r adminNetworkStatusResult) RenderTextWithOptions(w io.Writer, opts cli.TableOptions) error {
	if len(r.Bound) > 0 {
		if _, err := fmt.Fprintln(w, "TCP listeners:"); err != nil {
			return err
		}
		if err := cli.WriteTableWithOptions(w, r.Bound, opts); err != nil {
			return err
		}
	}
	if len(r.LocalIPCAddresses) > 0 {
		clean := make([]string, len(r.LocalIPCAddresses))
		for i, addr := range r.LocalIPCAddresses {
			clean[i] = cli.SanitizeSingleLine(addr)
		}
		if _, err := fmt.Fprintf(w, "\nLocal IPC: %s\n", strings.Join(clean, ", ")); err != nil {
			return err
		}
	}
	if len(r.ConfiguredExtra) > 0 {
		clean := make([]string, len(r.ConfiguredExtra))
		for i, addr := range r.ConfiguredExtra {
			clean[i] = cli.SanitizeSingleLine(addr)
		}
		if _, err := fmt.Fprintf(w, "Extra listen addresses: %s\n", strings.Join(clean, ", ")); err != nil {
			return err
		}
	}
	if len(r.Interfaces) > 0 {
		if _, err := fmt.Fprintln(w, "\nInterfaces:"); err != nil {
			return err
		}
		for _, iface := range r.Interfaces {
			state := "down"
			if iface.Up {
				state = "up"
			}
			ips := make([]string, 0, len(iface.Addresses))
			for _, a := range iface.Addresses {
				tag := cli.SanitizeSingleLine(a.IP)
				if a.Loopback {
					tag += " (loopback)"
				}
				ips = append(ips, tag)
			}
			if _, err := fmt.Fprintf(w, "  %-10s %-4s %s\n", cli.SanitizeSingleLine(iface.Name), state, strings.Join(ips, ", ")); err != nil {
				return err
			}
		}
	}
	return nil
}

func runAdminNetworkStatus(inv *cli.Invocation) (any, error) {
	c, err := requireAdminClient(inv)
	if err != nil {
		return nil, err
	}
	resp, err := c.AdminNetworkService().GetListenStatus(inv.Context, &leapmuxv1.GetListenStatusRequest{})
	if err != nil {
		return nil, err
	}
	bound := make([]adminBoundAddressRow, 0, len(resp.GetBound()))
	for _, b := range resp.GetBound() {
		bound = append(bound, adminBoundAddressRow{
			Address: b.GetAddress(),
			Source:  b.GetSource(),
			Error:   b.GetError(),
		})
	}
	ifaces := make([]adminNetworkInterfaceRow, 0, len(resp.GetInterfaces()))
	for _, iface := range resp.GetInterfaces() {
		addrs := make([]adminNetworkInterfaceAddr, 0, len(iface.GetAddresses()))
		for _, a := range iface.GetAddresses() {
			addrs = append(addrs, adminNetworkInterfaceAddr{
				IP:       a.GetIp(),
				IPv6:     a.GetIpv6(),
				Loopback: a.GetLoopback(),
			})
		}
		ifaces = append(ifaces, adminNetworkInterfaceRow{
			Name:      iface.GetName(),
			Up:        iface.GetUp(),
			Addresses: addrs,
		})
	}
	return adminNetworkStatusResult{
		BaseTCPAddresses:  resp.GetBaseTcpAddresses(),
		ConfiguredExtra:   resp.GetConfigured(),
		LocalIPCAddresses: resp.GetLocalIpcAddresses(),
		Bound:             bound,
		Interfaces:        ifaces,
	}, nil
}
