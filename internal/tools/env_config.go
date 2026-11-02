package tools

import (
	"os"
	"strings"
)

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envBool(keys ...string) bool {
	v := strings.ToLower(envFirst(keys...))
	return v == "true" || v == "1" || v == "yes"
}
