package api

import (
	"net"
	"net/http"
	"strings"
)

func requestIPInfo(r *http.Request) (string, []string) {
	all := make([]string, 0, 8)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range all {
			if existing == value {
				return
			}
		}
		all = append(all, value)
	}
	addCSV := func(value string) {
		for _, item := range strings.Split(value, ",") {
			add(normalizeIPToken(item))
		}
	}

	add(normalizeIPToken(r.Header.Get("CF-Connecting-IP")))
	add(normalizeIPToken(r.Header.Get("True-Client-IP")))
	addCSV(r.Header.Get("X-Forwarded-For"))
	add(normalizeIPToken(r.Header.Get("X-Real-IP")))
	addForwardedFor(r.Header.Get("Forwarded"), add)
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		add(normalizeIPToken(host))
	} else {
		add(normalizeIPToken(r.RemoteAddr))
	}
	if len(all) == 0 {
		return "", nil
	}
	return all[0], all
}

func normalizeIPToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"")
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "for=") {
		value = strings.TrimSpace(value[4:])
		value = strings.Trim(value, "\"")
	}
	if strings.HasPrefix(value, "[") && strings.Contains(value, "]") {
		end := strings.Index(value, "]")
		if end > 1 {
			return strings.TrimSpace(value[1:end])
		}
	}
	if host, _, err := net.SplitHostPort(value); err == nil && host != "" {
		return strings.TrimSpace(host)
	}
	return value
}

func addForwardedFor(value string, add func(string)) {
	for _, part := range strings.Split(value, ",") {
		for _, section := range strings.Split(part, ";") {
			section = strings.TrimSpace(section)
			if strings.HasPrefix(strings.ToLower(section), "for=") {
				add(normalizeIPToken(section))
			}
		}
	}
}
