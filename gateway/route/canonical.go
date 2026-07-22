package route

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

var errAbsolutePath = errors.New("route: path must be absolute")

// CanonicalPath removes parameter names while retaining the complete static
// route shape. It is used only for route identity, never for dispatch.
func CanonicalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", errAbsolutePath
	}
	if path == "/" {
		return path, nil
	}
	path = strings.TrimSuffix(path, "/")
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, segment := range segments {
		if segment == "" {
			return "", errors.New("route: path contains an empty segment")
		}
		switch segment[0] {
		case ':':
			cleanSegment := strings.TrimSuffix(segment, "?")
			name := cleanSegment[1:]
			if name == "" || strings.ContainsAny(name, ":*?") {
				return "", errors.New("route: parameter name is required")
			}
			if strings.HasSuffix(segment, "?") {
				if index != len(segments)-1 {
					return "", errors.New("route: optional parameter must be the final segment")
				}
				segments[index] = ":?"
			} else {
				segments[index] = ":"
			}
		case '*':
			name := segment[1:]
			if strings.ContainsAny(name, ":*?") {
				return "", errors.New("route: invalid catch-all parameter")
			}
			if index != len(segments)-1 {
				return "", errors.New("route: catch-all must be the final segment")
			}
			segments[index] = "*"
		}
	}
	return "/" + strings.Join(segments, "/"), nil
}

// SlotID returns a stable identity for one HTTP method and canonical path.
func SlotID(method, path string) (string, error) {
	canonicalPath, err := CanonicalPath(path)
	if err != nil {
		return "", err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return "", errors.New("route: method is required")
	}
	return hash(method + "|" + canonicalPath), nil
}

// OwnerID returns a stable opaque identity for a route publisher.
func OwnerID(owner string) string {
	return hash(strings.TrimSpace(owner))
}

// MatchPrefix reports whether path falls under at least one complete path
// segment prefix. It never treats /api as a prefix of /apix.
func MatchPrefix(path string, prefixes []string) bool {
	path, err := CanonicalPath(path)
	if err != nil {
		return false
	}
	for _, prefix := range prefixes {
		prefix, err = CanonicalPath(prefix)
		if err != nil {
			continue
		}
		if prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func hash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
