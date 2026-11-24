package domain

import (
	"fmt"
	"strings"
)

// Limits that keep selector evaluation cheap and predictable.
const (
	MaxSelectorLength       = 2048
	MaxSelectorRequirements = 20
	MaxSelectorValues       = 50
	maxSelectorValueLength  = 253
	// MaxTargetRuleEntries bounds the include and exclude lists of a TargetRule.
	MaxTargetRuleEntries = 500
	// MaxPreviewHosts bounds how many hosts a preview lists in detail.
	MaxPreviewHosts = 500
)

type selectorOp int

const (
	opEquals selectorOp = iota
	opNotEquals
	opIn
	opNotIn
	opExists
	opNotExists
)

type requirement struct {
	key    string
	op     selectorOp
	values []string
}

// Selector is a parsed host selector: a comma-separated list of requirements
// that must all hold (logical AND).
//
//	environment=production            equals (== is accepted too)
//	tier!=legacy                      not equals; also true for hosts without the label
//	role in (web,db)                  in a set
//	role notin (batch,etl)            not in a set; also true for hosts without the label
//	canary                            the label exists
//	!canary                           the label does not exist
//
// Values may contain the wildcards * (any run of characters) and ? (one
// character), for example name=web-* . There are no regular expressions.
//
// Which fields exist, and which compare case-insensitively, is decided by the
// subject (see MatchesFields); for managed hosts see resource.Host.SelectorField.
//
// An empty expression is a parse error, and a nil *Selector matches nothing, so
// a rule can never select the whole fleet by accident.
type Selector struct {
	expr string
	reqs []requirement
}

// ParseSelector parses expr. Errors wrap ErrInvalidInput and are safe to show to users.
func ParseSelector(expr string) (*Selector, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("%w: selector is empty; use an explicit rule to target every host", ErrInvalidInput)
	}
	if len(expr) > MaxSelectorLength {
		return nil, fmt.Errorf("%w: selector is longer than %d characters", ErrInvalidInput, MaxSelectorLength)
	}
	parts, err := splitRequirements(expr)
	if err != nil {
		return nil, err
	}
	if len(parts) > MaxSelectorRequirements {
		return nil, fmt.Errorf("%w: selector has more than %d requirements", ErrInvalidInput, MaxSelectorRequirements)
	}
	selector := &Selector{expr: expr}
	for _, part := range parts {
		req, err := parseRequirement(part)
		if err != nil {
			return nil, err
		}
		selector.reqs = append(selector.reqs, req)
	}
	return selector, nil
}

// String returns the expression the selector was parsed from.
func (s *Selector) String() string {
	if s == nil {
		return ""
	}
	return s.expr
}

// SelectorField returns a field's value, whether it is present, and whether
// comparisons on it ignore case.
type SelectorField func(key string) (value string, present, fold bool)

// MatchesFields reports whether the subject described by field satisfies every
// requirement. It lets any resource type be selected with the same syntax.
func (s *Selector) MatchesFields(field SelectorField) bool {
	if s == nil || field == nil || len(s.reqs) == 0 {
		return false
	}
	for _, req := range s.reqs {
		value, present, fold := field(req.key)
		if !req.matchesValue(value, present, fold) {
			return false
		}
	}
	return true
}

func splitRequirements(expr string) ([]string, error) {
	var parts []string
	depth, start := 0, 0
	for i, r := range expr {
		switch r {
		case '(':
			depth++
			if depth > 1 {
				return nil, fmt.Errorf("%w: nested parentheses at position %d", ErrInvalidInput, i+1)
			}
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%w: unexpected ')' at position %d", ErrInvalidInput, i+1)
			}
		case ',':
			if depth == 0 {
				parts = append(parts, expr[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: unbalanced parentheses", ErrInvalidInput)
	}
	return append(parts, expr[start:]), nil
}

func parseRequirement(part string) (requirement, error) {
	part = strings.TrimSpace(part)
	if part == "" {
		return requirement{}, fmt.Errorf("%w: empty requirement (check for a doubled or trailing comma)", ErrInvalidInput)
	}

	if strings.HasPrefix(part, "!") && !strings.HasPrefix(part, "!=") {
		key := strings.TrimSpace(part[1:])
		if err := validateSelectorKey(key); err != nil {
			return requirement{}, err
		}
		return requirement{key: key, op: opNotExists}, nil
	}

	if i := strings.Index(part, "!="); i >= 0 {
		return comparison(part[:i], part[i+2:], opNotEquals)
	}
	if i := strings.Index(part, "=="); i >= 0 {
		return comparison(part[:i], part[i+2:], opEquals)
	}
	if i := strings.Index(part, "="); i >= 0 {
		return comparison(part[:i], part[i+1:], opEquals)
	}

	if open := strings.Index(part, "("); open >= 0 {
		head := strings.Fields(part[:open])
		if len(head) != 2 || (head[1] != "in" && head[1] != "notin") {
			return requirement{}, fmt.Errorf("%w: expected 'key in (a,b)' or 'key notin (a,b)' in %q", ErrInvalidInput, truncateForError(part))
		}
		if !strings.HasSuffix(part, ")") {
			return requirement{}, fmt.Errorf("%w: missing ')' in %q", ErrInvalidInput, truncateForError(part))
		}
		if err := validateSelectorKey(head[0]); err != nil {
			return requirement{}, err
		}
		raw := strings.Split(part[open+1:len(part)-1], ",")
		if len(raw) > MaxSelectorValues {
			return requirement{}, fmt.Errorf("%w: a set may list at most %d values", ErrInvalidInput, MaxSelectorValues)
		}
		values := make([]string, 0, len(raw))
		for _, value := range raw {
			value = strings.TrimSpace(value)
			if value == "" {
				return requirement{}, fmt.Errorf("%w: empty value in the set for %q", ErrInvalidInput, head[0])
			}
			if err := validateSelectorValue(value); err != nil {
				return requirement{}, err
			}
			values = append(values, value)
		}
		op := opIn
		if head[1] == "notin" {
			op = opNotIn
		}
		return requirement{key: head[0], op: op, values: values}, nil
	}

	if strings.ContainsAny(part, " \t)") {
		return requirement{}, fmt.Errorf("%w: cannot parse requirement %q", ErrInvalidInput, truncateForError(part))
	}
	if err := validateSelectorKey(part); err != nil {
		return requirement{}, err
	}
	return requirement{key: part, op: opExists}, nil
}

func comparison(rawKey, rawValue string, op selectorOp) (requirement, error) {
	key, value := strings.TrimSpace(rawKey), strings.TrimSpace(rawValue)
	if err := validateSelectorKey(key); err != nil {
		return requirement{}, err
	}
	if strings.ContainsAny(value, "=()!, \t") {
		return requirement{}, fmt.Errorf("%w: invalid value %q for %q", ErrInvalidInput, truncateForError(value), key)
	}
	if value != "" {
		if err := validateSelectorValue(value); err != nil {
			return requirement{}, err
		}
	}
	return requirement{key: key, op: op, values: []string{value}}, nil
}

func validateSelectorKey(key string) error {
	if key == "" || len(key) > MaxLabelKeyLength || !labelKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: invalid selector key %q", ErrInvalidInput, truncateForError(key))
	}
	return nil
}

func validateSelectorValue(value string) error {
	if len(value) > maxSelectorValueLength {
		return fmt.Errorf("%w: selector value is longer than %d characters", ErrInvalidInput, maxSelectorValueLength)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '/', r == ':', r == '*', r == '?':
		default:
			return fmt.Errorf("%w: selector value %q contains an unsupported character", ErrInvalidInput, truncateForError(value))
		}
	}
	return nil
}

func (r requirement) matchesValue(value string, present, fold bool) bool {
	switch r.op {
	case opExists:
		return present
	case opNotExists:
		return !present
	case opEquals, opIn:
		return present && anyGlob(r.values, value, fold)
	default: // opNotEquals, opNotIn
		return !present || !anyGlob(r.values, value, fold)
	}
}

func anyGlob(patterns []string, value string, fold bool) bool {
	if fold {
		value = strings.ToLower(value)
	}
	for _, pattern := range patterns {
		if fold {
			pattern = strings.ToLower(pattern)
		}
		if globMatch(pattern, value) {
			return true
		}
	}
	return false
}

// globMatch matches pattern, where * is any run of bytes and ? is one byte,
// against s in O(len(pattern)*len(s)) time without backtracking blow-up.
func globMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		// The wildcard must be checked first: a literal '*' in s must not
		// consume the pattern's '*' (found by FuzzGlobMatch).
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case star >= 0:
			mark++
			p, i = star+1, mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// TargetRule describes which hosts an operation applies to. At least one of
// Selector, Include, or All is required: an empty rule is an error, never "all".
//
// The targets are the hosts matched by Selector (or every host when All is set)
// plus the hosts named in Include, minus the hosts named in Exclude. Include and
// Exclude entries are host IDs or names (names ignore case).
type TargetRule struct {
	Selector string   `json:"selector,omitempty"`
	Include  []string `json:"include,omitempty"`
	Exclude  []string `json:"exclude,omitempty"`
	All      bool     `json:"all,omitempty"`
}

// Validate checks the rule's shape and parses its selector.
func (r TargetRule) Validate() error {
	_, err := r.parse()
	return err
}

// ParsedSelector validates the rule and returns its selector, which is nil
// when the rule has none (it then relies on All or Include).
func (r TargetRule) ParsedSelector() (*Selector, error) {
	return r.parse()
}

func (r TargetRule) parse() (*Selector, error) {
	hasSelector := strings.TrimSpace(r.Selector) != ""
	switch {
	case !hasSelector && len(r.Include) == 0 && !r.All:
		return nil, fmt.Errorf("%w: a target rule needs a selector, an include list, or all=true", ErrInvalidInput)
	case r.All && (hasSelector || len(r.Include) > 0):
		return nil, fmt.Errorf("%w: all=true cannot be combined with a selector or an include list", ErrInvalidInput)
	case len(r.Include) > MaxTargetRuleEntries || len(r.Exclude) > MaxTargetRuleEntries:
		return nil, fmt.Errorf("%w: include and exclude lists may hold at most %d entries", ErrInvalidInput, MaxTargetRuleEntries)
	}
	for _, list := range [][]string{r.Include, r.Exclude} {
		for _, entry := range list {
			if entry = strings.TrimSpace(entry); entry == "" || len(entry) > 255 {
				return nil, fmt.Errorf("%w: include and exclude entries must be host IDs or names of at most 255 characters", ErrInvalidInput)
			}
		}
	}
	if !hasSelector {
		return nil, nil
	}
	return ParseSelector(r.Selector)
}

// LabelOp is a label condition a store can evaluate with an index.
type LabelOp int

// Label conditions (see LabelConstraints).
const (
	LabelExists LabelOp = iota
	LabelNotExists
	LabelIn
	LabelNotIn
)

// LabelConstraint is one indexable condition on a label.
type LabelConstraint struct {
	Key    string
	Op     LabelOp
	Values []string // for LabelIn and LabelNotIn; never wildcards
}

// LabelConstraints translates the requirements on labels (keys for which
// builtin is false) into conditions a store can evaluate with an index
// (AUDIT A-12). Every subject the selector matches satisfies them all, so a
// store may use them to narrow candidates; the selector itself must still be
// applied to the result, which keeps wildcards and built-in fields exact.
//
// Equality and "in" become LabelIn (LabelExists with wildcards); inequality
// and "notin" become LabelNotIn (nothing with wildcards); existence maps
// directly. Label comparisons are case-sensitive, like the selector's.
func (s *Selector) LabelConstraints(builtin func(key string) bool) []LabelConstraint {
	if s == nil {
		return nil
	}
	var out []LabelConstraint
	for _, req := range s.reqs {
		if builtin(req.key) {
			continue
		}
		wild := false
		for _, value := range req.values {
			wild = wild || strings.ContainsAny(value, "*?")
		}
		switch req.op {
		case opExists:
			out = append(out, LabelConstraint{Key: req.key, Op: LabelExists})
		case opNotExists:
			out = append(out, LabelConstraint{Key: req.key, Op: LabelNotExists})
		case opEquals, opIn:
			if wild {
				out = append(out, LabelConstraint{Key: req.key, Op: LabelExists})
			} else {
				out = append(out, LabelConstraint{Key: req.key, Op: LabelIn, Values: append([]string(nil), req.values...)})
			}
		case opNotEquals, opNotIn:
			if !wild {
				out = append(out, LabelConstraint{Key: req.key, Op: LabelNotIn, Values: append([]string(nil), req.values...)})
			}
		}
	}
	return out
}
