package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/avast/apkverifier"
)

// lfsPointerPrefix is how a Git LFS pointer file starts. Such a small text
// file is left in place of the real file when a repository that uses Git LFS
// is cloned or built without fetching the LFS objects.
const lfsPointerPrefix = "version https://git-lfs.github.com/spec/"

// isLFSPointer reports whether the file at path is a Git LFS pointer file
// instead of the real content.
func isLFSPointer(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, len(lfsPointerPrefix))
	if _, err := io.ReadFull(f, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil // shorter than the prefix
		}
		return false, err
	}
	return string(buf) == lfsPointerPrefix, nil
}

// signingCerts returns the SHA-256 fingerprints of the certificates that
// signed the apk, sorted.
func signingCerts(path string) ([]string, error) {
	chains, err := apkverifier.ExtractCerts(path, nil)
	if err != nil {
		return nil, err
	}
	var fps []string
	for _, chain := range chains {
		if len(chain) == 0 {
			continue
		}
		sum := sha256.Sum256(chain[0].Raw)
		fp := hex.EncodeToString(sum[:])
		if !slices.Contains(fps, fp) {
			fps = append(fps, fp)
		}
	}
	slices.Sort(fps)
	return fps, nil
}

// signersDiffer compares the signing certificates of two apks. Android only
// installs an update over an existing app if it is signed with the same
// certificate. It returns false if either apk cannot be read.
func signersDiffer(pathA, pathB string) bool {
	a, errA := signingCerts(pathA)
	b, errB := signingCerts(pathB)
	if errA != nil || errB != nil || len(a) == 0 || len(b) == 0 {
		return false
	}
	return !slices.Equal(a, b)
}
