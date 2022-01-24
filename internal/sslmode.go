package internal

import "os"

func GetSSLMode() string {
	if os.Getenv("TREK_DISABLE_SSL") == "true" {
		return "disable"
	}

	return "require"
}
