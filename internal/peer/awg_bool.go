package peer

import (
	"fmt"
	"strconv"
	"strings"
)

// FormatAWGBool returns the canonical boolean representation accepted and
// emitted by current amneziawg-tools configuration files.
func FormatAWGBool(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

// ParseAWGBool accepts the canonical on/off representation as well as the
// numeric and legacy Go boolean forms used by earlier Cascade configurations.
func ParseAWGBool(value string) (bool, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid AmneziaWG boolean %q", value)
	}
	return parsed, nil
}
