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

func TestApplyLabelPatch(t *testing.T) {
	current := map[string]string{"role": "web", "tier": "1", "old": "x"}
	merged, err := ApplyLabelPatch(current, map[string]string{"role": "api", "new": "y"}, []string{"old", "absent"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"role": "api", "tier": "1", "new": "y"}
	if len(merged) != len(want) {
		t.Fatalf("merged = %v, want %v", merged, want)
	}
	for k, v := range want {
		if merged[k] != v {
			t.Errorf("merged[%s] = %q, want %q", k, merged[k], v)
		}
	}
	if current["role"] != "web" || len(current) != 3 {
		t.Error("the input labels must not be modified")
	}
}

func TestApplyLabelPatchRejectsBadPatches(t *testing.T) {
	current := map[string]string{"a": "1"}
	cases := map[string]struct {
		set    map[string]string
		remove []string
	}{
		"empty patch":      {},
		"set and remove":   {set: map[string]string{"a": "2"}, remove: []string{"a"}},
		"bad set key":      {set: map[string]string{"bad key": "v"}},
		"bad set value":    {set: map[string]string{"a": "v w"}},
		"reserved key":     {set: map[string]string{"status": "v"}},
		"empty remove key": {remove: []string{""}},
	}
	for name, tc := range cases {
		if _, err := ApplyLabelPatch(current, tc.set, tc.remove); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}

	full := map[string]string{}
	for i := 0; i < MaxLabels; i++ {
		full["key"+strings.Repeat("a", i/26)+string(rune('a'+i%26))] = "v"
	}
	if _, err := ApplyLabelPatch(full, map[string]string{"one-more": "v"}, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("exceeding %d labels error = %v, want ErrInvalidInput", MaxLabels, err)
	}
	// Removing one while adding one stays within the limit.
	var anyKey string
	for k := range full {
		anyKey = k
		break
	}
	if _, err := ApplyLabelPatch(full, map[string]string{"one-more": "v"}, []string{anyKey}); err != nil {
		t.Errorf("swap at the limit error = %v", err)
	}
	// A host holding a legacy (now invalid) label can still have it removed.
	if _, err := ApplyLabelPatch(map[string]string{"bad key!": "v"}, nil, []string{"bad key!"}); err != nil {
		t.Errorf("removing a legacy label error = %v", err)
	}
}

func TestSSHValidateRejectsInvalidLabels(t *testing.T) {
	host := &Host{
		Name: "web-01", IPAddress: "10.0.0.1", Labels: map[string]string{"name": "x"},
		SSH: &SSHAccess{Username: "u", AuthType: SSHAuthType{Method: AuthTypePassword, Password: "p", ExpectedFingerprint: "SHA256:x"}},
	}
	if err := host.SSHValidate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SSHValidate() = %v, want ErrInvalidInput for a reserved label key", err)
	}
	host.Labels = map[string]string{"role": "web"}
	if err := host.SSHValidate(); err != nil {
		t.Fatalf("SSHValidate() with valid labels = %v", err)
	}
}
