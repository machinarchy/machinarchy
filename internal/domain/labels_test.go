package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateLabels(t *testing.T) {
	valid := map[string]string{"role": "web", "team/squad": "blue-1", "owner": "", "v1.2": "a_b.c"}
	if err := ValidateLabels(valid); err != nil {
		t.Fatalf("ValidateLabels(valid) = %v", err)
	}
	if err := ValidateLabels(nil); err != nil {
		t.Errorf("nil labels = %v", err)
	}

	tooMany := map[string]string{}
	for i := 0; i <= MaxLabels; i++ {
		tooMany["k"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	for name, labels := range map[string]map[string]string{
		"empty key":          {"": "v"},
		"key with space":     {"a b": "v"},
		"key edge dash":      {"-a": "v"},
		"key too long":       {strings.Repeat("a", MaxLabelKeyLength+1): "v"},
		"value too long":     {"a": strings.Repeat("v", MaxLabelValueLength+1)},
		"value with space":   {"a": "x y"},
		"value with star":    {"a": "web-*"},
		"value edge dot":     {"a": ".x"},
		"reserved name":      {"name": "v"},
		"reserved mixedcase": {"Environment": "v"},
		"too many labels":    tooMany,
	} {
		if err := ValidateLabels(labels); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}
