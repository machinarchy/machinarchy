package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// MaxTargetHosts bounds how many managed Hosts one target resolution scans.
const MaxTargetHosts = 10000

// ErrTargetsChanged reports that the Hosts a rule resolves to differ from the
// set the caller previewed and approved.
var ErrTargetsChanged = errors.New("the targeted hosts changed since they were previewed")

// IsHostBuiltinField reports whether key is a built-in Host selector field
// rather than a label (see SelectorField).
func IsHostBuiltinField(key string) bool {
	switch key {
	case "id", "name", "environment", "phase", "presence", "profile":
		return true
	}
	return false
}

// SelectorField exposes a managed Host to the shared selector syntax.
// Built-in fields: id, name, environment, phase, presence and profile (the
// Access Profile ID); name, environment, phase and presence ignore case. Any
// other key is a label (case-sensitive).
func (h *Host) SelectorField(environmentName string) domain.SelectorField {
	return func(key string) (string, bool, bool) {
		switch key {
		case "id":
			return string(h.Metadata.ID), true, false
		case "name":
			return h.Metadata.Name, h.Metadata.Name != "", true
		case "environment":
			return environmentName, environmentName != "", true
		case "phase":
			return string(h.Status.Phase), h.Status.Phase != "", true
		case "presence":
			return string(h.Spec.Presence), h.Spec.Presence != "", true
		case "profile":
			return string(h.Spec.AccessProfileID), h.Spec.AccessProfileID != "", false
		}
		value, present := h.Metadata.Labels[key]
		return value, present, false
	}
}

// HostTargetSet is the result of resolving a TargetRule against managed Hosts.
type HostTargetSet struct {
	Matched         []Host   `json:"hosts"`
	Excluded        []Host   `json:"excluded"`
	UnknownIncludes []string `json:"unknownIncludes"`
	UnknownExcludes []string `json:"unknownExcludes"`
	// TargetsHash identifies the exact Host IDs and generations matched, so a
	// submission can require that it runs on what was reviewed.
	TargetsHash string `json:"targetsHash"`
}

// ResolveHostTargets applies rule to hosts. Include and Exclude entries are
// Host IDs or names (names ignore case). environments maps IDs to names for
// the "environment" field and may be nil.
func ResolveHostTargets(rule domain.TargetRule, hosts []Host, environments map[int]string) (*HostTargetSet, error) {
	selector, err := rule.ParsedSelector()
	if err != nil {
		return nil, err
	}
	byID := make(map[ID]int, len(hosts))
	byName := make(map[string]int, len(hosts))
	for i := range hosts {
		byID[hosts[i].Metadata.ID] = i
		byName[strings.ToLower(hosts[i].Metadata.Name)] = i
	}
	lookup := func(entry string) (int, bool) {
		entry = strings.TrimSpace(entry)
		if i, ok := byID[ID(entry)]; ok {
			return i, true
		}
		i, ok := byName[strings.ToLower(entry)]
		return i, ok
	}

	selected := map[int]bool{}
	for i := range hosts {
		if rule.All || selector.MatchesFields(hosts[i].SelectorField(environments[hosts[i].Metadata.EnvironmentID])) {
			selected[i] = true
		}
	}
	set := &HostTargetSet{Matched: []Host{}, Excluded: []Host{}, UnknownIncludes: []string{}, UnknownExcludes: []string{}}
	for _, entry := range rule.Include {
		if i, ok := lookup(entry); ok {
			selected[i] = true
		} else {
			set.UnknownIncludes = append(set.UnknownIncludes, strings.TrimSpace(entry))
		}
	}
	excluded := map[int]bool{}
	for _, entry := range rule.Exclude {
		if i, ok := lookup(entry); ok {
			excluded[i] = true
		} else {
			set.UnknownExcludes = append(set.UnknownExcludes, strings.TrimSpace(entry))
		}
	}
	for i := range selected {
		if excluded[i] {
			set.Excluded = append(set.Excluded, hosts[i])
		} else {
			set.Matched = append(set.Matched, hosts[i])
		}
	}
	sortHostsByName(set.Matched)
	sortHostsByName(set.Excluded)
	set.TargetsHash = HostTargetsHash(set.Matched)
	return set, nil
}

// HostTargetsHash digests the Host IDs and generations, independent of order.
func HostTargetsHash(hosts []Host) string {
	keys := make([]string, len(hosts))
	for i, host := range hosts {
		keys[i] = string(host.Metadata.ID) + "@" + strconv.FormatUint(host.Metadata.Generation, 10)
	}
	sort.Strings(keys)
	digest := sha256.New()
	for _, key := range keys {
		_, _ = digest.Write([]byte(strconv.Itoa(len(key)) + ":" + key + "|"))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func sortHostsByName(hosts []Host) {
	sort.Slice(hosts, func(i, j int) bool {
		a, b := strings.ToLower(hosts[i].Metadata.Name), strings.ToLower(hosts[j].Metadata.Name)
		if a != b {
			return a < b
		}
		return hosts[i].Metadata.ID < hosts[j].Metadata.ID
	})
}
