package domain

import (
	"encoding/json"
	"testing"
)

func TestRoleAllows(t *testing.T) {
	tests := []struct {
		role       Role
		permission Permission
		want       bool
	}{
		{RoleOwner, PermissionTokenManage, true},
		{RoleOwner, PermissionMembersManage, true},
		{RoleOperator, PermissionMembersManage, false},
		{RoleReader, PermissionMembersManage, false},
		{RoleOperator, PermissionInventoryRead, true},
		{RoleOperator, PermissionInventoryWrite, true},
		{RoleOperator, PermissionTokenManage, false},
		{RoleReader, PermissionOperationsRead, true},
		{RoleReader, PermissionInventoryWrite, false},
		{Role("unknown"), PermissionInventoryRead, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.permission), func(t *testing.T) {
			if got := tt.role.Allows(tt.permission); got != tt.want {
				t.Fatalf("Allows() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestEffectiveRoleUsesLeastPrivilege(t *testing.T) {
	tests := []struct {
		credential Role
		member     Role
		want       Role
	}{
		{RoleOwner, RoleOwner, RoleOwner},
		{RoleOperator, RoleOwner, RoleOperator},
		{RoleOwner, RoleReader, RoleReader},
		{RoleReader, RoleOperator, RoleReader},
		{Role("invalid"), RoleOwner, ""},
	}
	for _, tt := range tests {
		if got := EffectiveRole(tt.credential, tt.member); got != tt.want {
			t.Errorf("EffectiveRole(%q, %q) = %q, want %q", tt.credential, tt.member, got, tt.want)
		}
	}
}

func TestPrincipalIsNotSerialized(t *testing.T) {
	encoded, err := json.Marshal(Principal{
		UserID: 7, WorkspaceID: 11, Role: RoleOwner, APITokenID: 42, Username: "owner",
	})
	if err != nil {
		t.Fatalf("marshal Principal: %v", err)
	}
	if got, want := string(encoded), "{}"; got != want {
		t.Fatalf("marshaled Principal = %s, want %s", got, want)
	}
}
