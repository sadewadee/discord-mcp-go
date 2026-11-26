package tools

import "testing"

func TestParsePermissions(t *testing.T) {
	tests := []struct {
		name, raw, names string
		want             int64
		wantErr          string
	}{
		{name: "empty", want: 0},
		{name: "raw", raw: "3072", want: 3072},
		{name: "names", names: "VIEW_CHANNEL,MESSAGE_SEND", want: 1<<10 | 1<<11},
		{name: "names with spaces", names: " VIEW_CHANNEL , VOICE_CONNECT ", want: 1<<10 | 1<<20},
		{name: "alias bit", names: "MANAGE_PERMISSIONS", want: 1 << 28},
		{name: "high bit", names: "PIN_MESSAGES", want: 1 << 51},
		{name: "both", raw: "1", names: "VIEW_CHANNEL", wantErr: "Cannot specify both raw bitfield and permission names for the same field"},
		{name: "bad raw", raw: "abc", wantErr: "Invalid permission bitfield: abc"},
		{name: "bad name", names: "VIEW_CHANNEL, NOPE", wantErr: "Invalid permission name: NOPE"},
		{name: "case sensitive", names: "view_channel", wantErr: "Invalid permission name: view_channel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePermissions(tc.raw, tc.names)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFormatPermissions(t *testing.T) {
	tests := []struct {
		raw  int64
		want string
	}{
		{0, "none"},
		{1 << 10, "View Channels"},
		{1<<10 | 1<<11, "View Channels, Send Messages"},
		{1 << 28, "Manage Roles, Manage Permissions"},
		{1 << 39, "Use Activities"},
		{1<<3 | 1<<4, "Manage Channels, Administrator"},
	}
	for _, tc := range tests {
		if got := formatPermissions(tc.raw); got != tc.want {
			t.Errorf("formatPermissions(%d) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestPermissionNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range permissionDefs {
		if seen[p.Name] {
			t.Errorf("duplicate permission name %s", p.Name)
		}
		seen[p.Name] = true
	}
}
