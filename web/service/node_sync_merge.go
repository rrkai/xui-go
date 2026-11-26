package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/alireza0/x-ui/database/model"
)

type syncEntry struct {
	fp       string
	ts       int64
	deleted  bool
	inbound  *model.Inbound
	outbound *model.Outbound
	rule     *model.RoutingRule
}

type SyncKindCount struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

type syncMergeSummary struct {
	Inbounds     SyncKindCount
	Outbounds    SyncKindCount
	RoutingRules SyncKindCount
	Warnings     []string
}

type syncMergeResult struct {
	Data    *SyncDataSet
	Summary syncMergeSummary
}

func mergeSyncData(base, local, remote *SyncDataSet, localPanelId, remotePanelId string) *syncMergeResult {
	if base == nil {
		base = &SyncDataSet{}
	}
	result := &syncMergeResult{Data: &SyncDataSet{State: make(map[string]SyncItemState)}}
	kinds := []string{SyncKindInbound, SyncKindOutbound, SyncKindRule}
	mergedByKind := make(map[string]map[string]*syncEntry, len(kinds))
	localByKind := make(map[string]map[string]*syncEntry, len(kinds))
	for _, kind := range kinds {
		localEntries := buildSyncEntries(kind, local)
		merged := mergeSyncEntries(buildSyncEntries(kind, base), localEntries, buildSyncEntries(kind, remote), localPanelId, remotePanelId)
		mergedByKind[kind] = merged
		localByKind[kind] = localEntries
		for key, entry := range merged {
			result.Data.State[SyncStateKey(kind, key)] = SyncItemState{ChangedAt: entry.ts, Deleted: entry.deleted}
		}
	}

	mergedInbounds, inboundWarnings := buildMergedInbounds(mergedByKind[SyncKindInbound])
	result.Data.Inbounds = mergedInbounds
	result.Data.Outbounds = buildMergedOutbounds(mergedByKind[SyncKindOutbound])
	result.Data.RoutingRules = buildMergedRules(mergedByKind[SyncKindRule])
	result.Summary.Warnings = append(result.Summary.Warnings, inboundWarnings...)
	result.Summary.Inbounds = countKindChanges(mergedByKind[SyncKindInbound], localByKind[SyncKindInbound])
	result.Summary.Outbounds = countKindChanges(mergedByKind[SyncKindOutbound], localByKind[SyncKindOutbound])
	result.Summary.RoutingRules = countKindChanges(mergedByKind[SyncKindRule], localByKind[SyncKindRule])
	return result
}

func buildSyncEntries(kind string, data *SyncDataSet) map[string]*syncEntry {
	entries := make(map[string]*syncEntry, 32)
	if data == nil {
		return entries
	}
	stateFor := func(key string) SyncItemState {
		if data.State == nil {
			return SyncItemState{}
		}
		return data.State[SyncStateKey(kind, key)]
	}
	switch kind {
	case SyncKindInbound:
		for _, inbound := range data.Inbounds {
			if inbound == nil {
				continue
			}
			tag := inboundEntryTag(inbound)
			st := stateFor(tag)
			entries[tag] = &syncEntry{fp: inboundFingerprint(inbound), ts: st.ChangedAt, inbound: inbound}
		}
	case SyncKindOutbound:
		for _, outbound := range data.Outbounds {
			if outbound == nil {
				continue
			}
			tag := outboundEntryTag(outbound)
			st := stateFor(tag)
			entries[tag] = &syncEntry{fp: outboundFingerprint(outbound), ts: st.ChangedAt, outbound: outbound}
		}
	case SyncKindRule:
		for _, rule := range data.RoutingRules {
			if rule == nil {
				continue
			}
			tag := ruleEntryTag(rule)
			st := stateFor(tag)
			entries[tag] = &syncEntry{fp: ruleFingerprint(rule), ts: st.ChangedAt, rule: rule}
		}
	}
	for fullKey, st := range data.State {
		stateKind, key := splitSyncStateKey(fullKey)
		if stateKind != kind || key == "" || !st.Deleted {
			continue
		}
		if _, ok := entries[key]; ok {
			continue
		}
		entries[key] = &syncEntry{ts: st.ChangedAt, deleted: true}
	}
	return entries
}

func entryEqual(a, b *syncEntry) bool {
	aAbsent := a == nil || a.deleted
	bAbsent := b == nil || b.deleted
	if aAbsent || bAbsent {
		return aAbsent && bAbsent
	}
	return a.fp == b.fp
}

func laterEntry(a, b *syncEntry) *syncEntry {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.ts > a.ts {
		return b
	}
	return a
}

func decideEntry(base, local, remote *syncEntry, localPanelId, remotePanelId string) *syncEntry {
	if entryEqual(local, remote) {
		return laterEntry(local, remote)
	}
	if entryEqual(local, base) {
		return remote
	}
	if entryEqual(remote, base) {
		return local
	}
	if local == nil {
		return remote
	}
	if remote == nil {
		return local
	}
	if local.ts != remote.ts {
		if local.ts > remote.ts {
			return local
		}
		return remote
	}
	if localPanelId >= remotePanelId {
		return local
	}
	return remote
}

func mergeSyncEntries(base, local, remote map[string]*syncEntry, localPanelId, remotePanelId string) map[string]*syncEntry {
	merged := make(map[string]*syncEntry, len(local))
	for key := range local {
		merged[key] = decideEntry(base[key], local[key], remote[key], localPanelId, remotePanelId)
	}
	for key := range remote {
		if _, ok := merged[key]; ok {
			continue
		}
		merged[key] = decideEntry(base[key], local[key], remote[key], localPanelId, remotePanelId)
	}
	for key := range base {
		if _, ok := merged[key]; ok {
			continue
		}
		merged[key] = decideEntry(base[key], local[key], remote[key], localPanelId, remotePanelId)
	}
	for key, entry := range merged {
		if entry == nil {
			delete(merged, key)
		}
	}
	return merged
}

func countKindChanges(merged, local map[string]*syncEntry) SyncKindCount {
	count := SyncKindCount{}
	for key, entry := range merged {
		if entry.deleted {
			continue
		}
		current := local[key]
		if current == nil || current.deleted {
			count.Added++
			continue
		}
		if current.fp != entry.fp {
			count.Updated++
		}
	}
	for key, entry := range local {
		if entry.deleted {
			continue
		}
		target := merged[key]
		if target == nil || target.deleted {
			count.Deleted++
		}
	}
	return count
}

func buildMergedInbounds(merged map[string]*syncEntry) ([]*model.Inbound, []string) {
	items := make([]*syncEntry, 0, len(merged))
	for _, entry := range merged {
		if entry.deleted || entry.inbound == nil {
			continue
		}
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].inbound.Port != items[j].inbound.Port {
			return items[i].inbound.Port < items[j].inbound.Port
		}
		return inboundEntryTag(items[i].inbound) < inboundEntryTag(items[j].inbound)
	})

	warnings := make([]string, 0)
	usedPorts := make(map[int]*syncEntry, len(items))
	result := make([]*model.Inbound, 0, len(items))
	for _, entry := range items {
		port := entry.inbound.Port
		if port <= 0 {
			result = append(result, entry.inbound)
			continue
		}
		existing, ok := usedPorts[port]
		if !ok {
			usedPorts[port] = entry
			result = append(result, entry.inbound)
			continue
		}
		winner := existing
		loser := entry
		if entry.ts > existing.ts || (entry.ts == existing.ts && inboundEntryTag(entry.inbound) < inboundEntryTag(existing.inbound)) {
			winner = entry
			loser = existing
		}
		warnings = append(warnings, fmt.Sprintf("端口冲突：保留 %s，跳过 %s（端口 %d）",
			inboundEntryTag(winner.inbound), inboundEntryTag(loser.inbound), port))
		if winner != existing {
			usedPorts[port] = winner
			for i := range result {
				if result[i] == existing.inbound {
					result[i] = winner.inbound
					break
				}
			}
		}
	}
	return result, warnings
}

func buildMergedOutbounds(merged map[string]*syncEntry) []*model.Outbound {
	items := make([]*syncEntry, 0, len(merged))
	for _, entry := range merged {
		if entry.deleted || entry.outbound == nil {
			continue
		}
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].outbound.Sort != items[j].outbound.Sort {
			return items[i].outbound.Sort < items[j].outbound.Sort
		}
		return outboundEntryTag(items[i].outbound) < outboundEntryTag(items[j].outbound)
	})
	result := make([]*model.Outbound, 0, len(items))
	for index, entry := range items {
		cloned := *entry.outbound
		cloned.Sort = index
		result = append(result, &cloned)
	}
	return result
}

func buildMergedRules(merged map[string]*syncEntry) []*model.RoutingRule {
	items := make([]*syncEntry, 0, len(merged))
	for _, entry := range merged {
		if entry.deleted || entry.rule == nil {
			continue
		}
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].rule.Sort != items[j].rule.Sort {
			return items[i].rule.Sort < items[j].rule.Sort
		}
		return ruleEntryTag(items[i].rule) < ruleEntryTag(items[j].rule)
	})
	result := make([]*model.RoutingRule, 0, len(items))
	for index, entry := range items {
		cloned := *entry.rule
		cloned.Id = 0
		cloned.Sort = index
		result = append(result, &cloned)
	}
	return result
}

func inboundEntryTag(inbound *model.Inbound) string {
	if inbound.Tag != "" {
		return inbound.Tag
	}
	if inbound.Listen == "" || inbound.Listen == "0.0.0.0" || inbound.Listen == "::" || inbound.Listen == "::0" {
		return fmt.Sprintf("inbound-%v", inbound.Port)
	}
	return fmt.Sprintf("inbound-%v:%v", inbound.Listen, inbound.Port)
}

func outboundEntryTag(outbound *model.Outbound) string {
	if outbound.Tag != "" {
		return outbound.Tag
	}
	return "outbound-" + outboundFingerprint(outbound)[:12]
}

func ruleEntryTag(rule *model.RoutingRule) string {
	if rule.Tag != "" {
		return rule.Tag
	}
	return "rule-" + ruleFingerprint(rule)[:12]
}

func inboundFingerprint(inbound *model.Inbound) string {
	raw := fmt.Sprintf("%t\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d",
		inbound.Enable, inbound.Listen, inbound.Port, inbound.Protocol,
		inbound.Settings, inbound.StreamSettings, inbound.Sniffing,
		inbound.Remark, inbound.Total, inbound.ExpiryTime, inbound.SpeedUp, inbound.SpeedDown)
	return hashSyncContent(raw)
}

func outboundFingerprint(outbound *model.Outbound) string {
	raw := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		outbound.Tag, outbound.SendThrough, outbound.Protocol, outbound.Settings,
		outbound.StreamSettings, outbound.ProxySettings, outbound.Mux, outbound.TargetStrategy)
	return hashSyncContent(raw)
}

func ruleFingerprint(rule *model.RoutingRule) string {
	return hashSyncContent(rule.Tag + "\x00" + canonicalRule(rule.RawJson))
}

func hashSyncContent(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// liveSyncFingerprints returns tag -> content fingerprint for live items,
// used to decide whether an apply is actually needed.
func liveSyncFingerprints(kind string, data *SyncDataSet) map[string]string {
	entries := buildSyncEntries(kind, data)
	result := make(map[string]string, len(entries))
	for key, entry := range entries {
		if entry.deleted {
			continue
		}
		result[key] = entry.fp
	}
	return result
}

func orderedLiveTags(kind string, data *SyncDataSet) []string {
	tags := make([]string, 0)
	if data == nil {
		return tags
	}
	switch kind {
	case SyncKindInbound:
		for _, inbound := range data.Inbounds {
			if inbound != nil {
				tags = append(tags, inboundEntryTag(inbound))
			}
		}
	case SyncKindOutbound:
		for _, outbound := range data.Outbounds {
			if outbound != nil {
				tags = append(tags, outboundEntryTag(outbound))
			}
		}
	case SyncKindRule:
		for _, rule := range data.RoutingRules {
			if rule != nil {
				tags = append(tags, ruleEntryTag(rule))
			}
		}
	}
	return tags
}

func datasetContentEqual(current, target *SyncDataSet) bool {
	kinds := []string{SyncKindInbound, SyncKindOutbound, SyncKindRule}
	for _, kind := range kinds {
		currentFP := liveSyncFingerprints(kind, current)
		targetFP := liveSyncFingerprints(kind, target)
		if len(currentFP) != len(targetFP) {
			return false
		}
		for tag, fp := range targetFP {
			if currentFP[tag] != fp {
				return false
			}
		}
		// Order is semantic for outbounds and routing rules.
		if kind != SyncKindInbound {
			currentOrder := orderedLiveTags(kind, current)
			targetOrder := orderedLiveTags(kind, target)
			if len(currentOrder) != len(targetOrder) {
				return false
			}
			for i := range currentOrder {
				if currentOrder[i] != targetOrder[i] {
					return false
				}
			}
		}
	}
	return true
}
