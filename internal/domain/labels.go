package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Const defines the maximum limits for labels.
const (
	// MaxLabels is the most labels one host may carry.
	MaxLabels = 50
	// MaxLabelKeyLength and MaxLabelValueLength bound label text.
	MaxLabelKeyLength   = 63
	MaxLabelValueLength = 63
)

var (
	labelKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]*[A-Za-z0-9])?$`)
	labelValuePattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?)?$`)

	// reservedLabelKeys are built-in selector fields. A label with one of these
	// names would be shadowed in selectors, so they cannot be used as labels.
	reservedLabelKeys = map[string]struct{}{
		"id": {}, "name": {}, "hostname": {}, "address": {},
		"status": {}, "environment": {}, "connection": {},
	}
)

// ValidateLabelKey reports whether key is a legal label key.
func ValidateLabelKey(key string) error {
	switch {
	case key == "" || len(key) > MaxLabelKeyLength || !labelKeyPattern.MatchString(key):
		return fmt.Errorf("%w: label key %q must be 1-%d characters of letters, digits, '.', '_', '-', or '/', starting and ending with a letter or digit",
			ErrInvalidInput, truncateForError(key), MaxLabelKeyLength)
	}
	if _, reserved := reservedLabelKeys[strings.ToLower(key)]; reserved {
		return fmt.Errorf("%w: label key %q is reserved for built-in selector fields", ErrInvalidInput, key)
	}
	return nil
}

// ValidateLabelValue reports whether value is a legal label value (which may be empty).
func ValidateLabelValue(key, value string) error {
	if len(value) > MaxLabelValueLength || !labelValuePattern.MatchString(value) {
		return fmt.Errorf("%w: value of label %q must be at most %d characters of letters, digits, '.', '_', or '-', starting and ending with a letter or digit",
			ErrInvalidInput, key, MaxLabelValueLength)
	}
	return nil
}

// ValidateLabels checks a complete label set.
func ValidateLabels(labels map[string]string) error {
	if len(labels) > MaxLabels {
		return fmt.Errorf("%w: a host may have at most %d labels", ErrInvalidInput, MaxLabels)
	}
	for _, key := range sortedKeys(labels) {
		if err := ValidateLabelKey(key); err != nil {
			return err
		}
		if err := ValidateLabelValue(key, labels[key]); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func truncateForError(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}
