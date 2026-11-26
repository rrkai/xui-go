//go:build linux

package ratelimit

import (
	"fmt"
	"sync"
	"unicode"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	outputChainName = "rl_out"
	inputChainName  = "rl_in"
)

var applyMu sync.Mutex

func Available() bool {
	return true
}

// Apply atomically replaces the rate-limit table for this panel. The table is
// dedicated to rate limiting, so other firewall tables and inbounds are never
// touched.
func Apply(tableName string, rules []Rule) error {
	if err := validateTableName(tableName); err != nil {
		return err
	}
	specs := normalizeRules(rules)

	applyMu.Lock()
	defer applyMu.Unlock()

	conn, err := nftables.New()
	if err != nil {
		return err
	}
	table := &nftables.Table{Name: tableName, Family: nftables.TableFamilyINet}
	exists, err := tableExists(conn, tableName)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		if !exists {
			return nil
		}
		conn.DelTable(table)
		return conn.Flush()
	}

	if exists {
		conn.DelTable(table)
	}

	policy := nftables.ChainPolicyAccept
	output := &nftables.Chain{
		Name:     outputChainName,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	}
	input := &nftables.Chain{
		Name:     inputChainName,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	}
	conn.AddTable(table)
	conn.AddChain(output)
	conn.AddChain(input)

	for _, spec := range specs {
		if spec.downBytes > 0 {
			conn.AddRule(&nftables.Rule{Table: table, Chain: output, Exprs: portLimitExprs(spec.port, unix.IPPROTO_TCP, 0, spec.downBytes)})
			conn.AddRule(&nftables.Rule{Table: table, Chain: output, Exprs: portLimitExprs(spec.port, unix.IPPROTO_UDP, 0, spec.downBytes)})
		}
		if spec.upBytes > 0 {
			conn.AddRule(&nftables.Rule{Table: table, Chain: input, Exprs: portLimitExprs(spec.port, unix.IPPROTO_TCP, 2, spec.upBytes)})
			conn.AddRule(&nftables.Rule{Table: table, Chain: input, Exprs: portLimitExprs(spec.port, unix.IPPROTO_UDP, 2, spec.upBytes)})
		}
	}
	return conn.Flush()
}

func tableExists(conn *nftables.Conn, name string) (bool, error) {
	tables, err := conn.ListTables()
	if err != nil {
		return false, err
	}
	for _, table := range tables {
		if table.Name == name && table.Family == nftables.TableFamilyINet {
			return true, nil
		}
	}
	return false, nil
}

// portLimitExprs matches a TCP/UDP port at the given transport offset (0 for
// source, 2 for destination) and drops packets above the byte rate.
func portLimitExprs(port uint16, proto byte, offset uint32, rate uint64) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: unix.NFT_REG32_00},
		&expr.Cmp{Op: expr.CmpOpEq, Register: unix.NFT_REG32_00, Data: []byte{proto}},
		&expr.Payload{
			DestRegister: unix.NFT_REG32_00,
			Base:         expr.PayloadBaseTransportHeader,
			Offset:       offset,
			Len:          2,
		},
		&expr.Cmp{Op: expr.CmpOpEq, Register: unix.NFT_REG32_00, Data: []byte{byte(port >> 8), byte(port)}},
		&expr.Limit{
			Type:  expr.LimitTypePktBytes,
			Rate:  rate,
			Over:  true,
			Unit:  expr.LimitTimeSecond,
			Burst: burstBytes(rate),
		},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

func burstBytes(rate uint64) uint32 {
	burst := rate / 10
	if burst < 32*1024 {
		burst = 32 * 1024
	}
	if burst > 4*1024*1024 {
		burst = 4 * 1024 * 1024
	}
	return uint32(burst)
}

func validateTableName(name string) error {
	if name == "" || len(name) > 32 {
		return fmt.Errorf("invalid rate limit table name: %q", name)
	}
	for i, r := range name {
		if r > unicode.MaxASCII || !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
			return fmt.Errorf("invalid rate limit table name: %q", name)
		}
	}
	return nil
}
