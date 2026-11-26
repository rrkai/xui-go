package ratelimit

import "sort"

// Rule limits the aggregate bandwidth of one inbound port. Rates are in
// kilobits per second; zero means unlimited in that direction.
type Rule struct {
	Port     int `json:"port"`
	UpKbps   int `json:"upKbps"`
	DownKbps int `json:"downKbps"`
}

type ruleSpec struct {
	port      uint16
	upBytes   uint64
	downBytes uint64
}

func kbpsToBytes(kbps int) uint64 {
	if kbps <= 0 {
		return 0
	}
	return uint64(kbps) * 125
}

// normalizeRules merges duplicate ports (taking the stricter limit) and drops
// rules that have no usable limit, so the nftables table only carries what is
// actually enforced.
func normalizeRules(rules []Rule) []ruleSpec {
	merged := make(map[int]Rule, len(rules))
	for _, rule := range rules {
		if rule.Port <= 0 || rule.Port > 65535 {
			continue
		}
		up := rule.UpKbps
		down := rule.DownKbps
		if up < 0 {
			up = 0
		}
		if down < 0 {
			down = 0
		}
		if up == 0 && down == 0 {
			continue
		}
		current, ok := merged[rule.Port]
		if !ok {
			merged[rule.Port] = Rule{Port: rule.Port, UpKbps: up, DownKbps: down}
			continue
		}
		current.UpKbps = tighterLimit(current.UpKbps, up)
		current.DownKbps = tighterLimit(current.DownKbps, down)
		merged[rule.Port] = current
	}
	specs := make([]ruleSpec, 0, len(merged))
	for _, rule := range merged {
		specs = append(specs, ruleSpec{
			port:      uint16(rule.Port),
			upBytes:   kbpsToBytes(rule.UpKbps),
			downBytes: kbpsToBytes(rule.DownKbps),
		})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].port < specs[j].port })
	return specs
}

func tighterLimit(current, next int) int {
	if next == 0 {
		return current
	}
	if current == 0 || next < current {
		return next
	}
	return current
}
