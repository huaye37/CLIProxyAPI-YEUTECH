package store

import (
	"fmt"
	"os"
	"testing"

	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"
)

func TestRepositoryCorruptionRecognizesMissingIndex(t *testing.T) {
	if !isRepositoryCorruptionError(fmt.Errorf("read pack index: %w", dotgit.ErrIdxNotFound)) {
		t.Fatal("missing Git pack index must be classified as repository corruption")
	}
	if isRepositoryCorruptionError(fmt.Errorf("packhandle: unrelated missing file: %w", os.ErrNotExist)) {
		t.Fatal("a packhandle message without a verified Git pack path must fail closed")
	}
}

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
