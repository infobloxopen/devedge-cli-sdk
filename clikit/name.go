package clikit

import (
	"fmt"
	"strings"
)

// ResourceName joins a collection and id into an AIP-122 resource name, e.g.
// ResourceName("widgets", "abc123") == "widgets/abc123".
func ResourceName(collection, id string) string {
	return collection + "/" + id
}

// ParseResourceName splits an AIP-122 resource name into its collection and id.
// It accepts a bare id (no slash) by returning ("", id, nil) so callers can
// pass either "widgets/abc123" or "abc123".
func ParseResourceName(name string) (collection, id string, err error) {
	name = strings.Trim(name, "/")
	if name == "" {
		return "", "", fmt.Errorf("empty resource name")
	}
	i := strings.LastIndexByte(name, '/')
	if i < 0 {
		return "", name, nil
	}
	return name[:i], name[i+1:], nil
}

// ResourceID returns just the id segment of a resource name (or the input when
// it is already a bare id).
func ResourceID(name string) string {
	_, id, err := ParseResourceName(name)
	if err != nil {
		return name
	}
	return id
}
