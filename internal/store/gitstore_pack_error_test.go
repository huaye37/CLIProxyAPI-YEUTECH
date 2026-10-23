package store

import (
	"fmt"
	"os"
	"testing"
)

func TestRepositoryCorruptionClassifiesOnlyMissingGitPacks(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		err  error
		want bool
	}{
		{"relative missing pack", "objects/pack/pack-deadbeef.pack", os.ErrNotExist, true},
		{"absolute missing pack", "/repo/.git/objects/pack/pack-deadbeef.pack", os.ErrNotExist, true},
		{"missing auth", "/repo/auths/account.json", os.ErrNotExist, false},
		{"missing config", "/repo/config/config.yaml", os.ErrNotExist, false},
		{"non-pack file", "objects/pack/other.json", os.ErrNotExist, false},
		{"pack permission", "objects/pack/pack-deadbeef.pack", os.ErrPermission, false},
		{"unrelated pack", "/uploads/pack-deadbeef.pack", os.ErrNotExist, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("packhandle: pack size: %w", &os.PathError{Op: "statat", Path: tc.path, Err: tc.err})
			if got := isRepositoryCorruptionError(err); got != tc.want {
				t.Fatalf("corruption = %v, want %v", got, tc.want)
			}
		})
	}
}
