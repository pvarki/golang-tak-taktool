package main

import (
	"slices"
	"strings"
)

// sortFold sorts strings case-insensitively.
func sortFold(s []string) {
	slices.SortFunc(s, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
}

// compareAtakFirst orders apps for lists: ATAK first, then by display name
// (case-insensitively).
func compareAtakFirst(pkgA, nameA, pkgB, nameB string) int {
	if isAtakApp(pkgA) != isAtakApp(pkgB) {
		if isAtakApp(pkgA) {
			return -1
		}
		return 1
	}
	return strings.Compare(strings.ToLower(nameA), strings.ToLower(nameB))
}
