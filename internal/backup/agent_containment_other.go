//go:build !windows

package backup

func enableAgentProcessContainment() error {
	return nil
}
