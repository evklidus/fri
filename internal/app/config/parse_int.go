package config

import "fmt"

func fmtSscanfImpl(value string, target *int) (int, error) {
	return fmt.Sscanf(value, "%d", target)
}
