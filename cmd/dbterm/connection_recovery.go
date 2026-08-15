package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/shreyam1008/dbterm/internal/config"
)

const connectionsRecoveryUsage = "usage: sudo dbterm connections recover-sudo"

func runConnectionsCommand(args []string) error {
	if len(args) != 1 || !strings.EqualFold(strings.TrimSpace(args[0]), "recover-sudo") {
		return fmt.Errorf(connectionsRecoveryUsage)
	}
	return recoverSudoConnections()
}

func mergeRecoveredConnections(current, recovered []config.ConnectionConfig) ([]config.ConnectionConfig, int, error) {
	merged := append([]config.ConnectionConfig(nil), current...)
	usedIDs := make(map[string]struct{}, len(merged)+len(recovered))
	for _, connection := range merged {
		if connection.ID != "" {
			usedIDs[connection.ID] = struct{}{}
		}
	}

	added := 0
	for _, candidate := range recovered {
		duplicate := false
		for _, existing := range merged {
			if recoveryEquivalent(existing, candidate) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}

		candidate.Active = false
		if candidate.ID == "" {
			id, err := newRecoveryConnectionID(usedIDs)
			if err != nil {
				return nil, added, err
			}
			candidate.ID = id
		} else if _, collision := usedIDs[candidate.ID]; collision {
			id, err := newRecoveryConnectionID(usedIDs)
			if err != nil {
				return nil, added, err
			}
			candidate.ID = id
		}
		usedIDs[candidate.ID] = struct{}{}
		merged = append(merged, candidate)
		added++
	}
	return merged, added, nil
}

func recoveryEquivalent(left, right config.ConnectionConfig) bool {
	left.ID, right.ID = "", ""
	left.LastUsed, right.LastUsed = "", ""
	left.Active, right.Active = false, false
	return left == right
}

func newRecoveryConnectionID(existing map[string]struct{}) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			return "", fmt.Errorf("generate recovered connection ID: %w", err)
		}
		id := "recovered-" + hex.EncodeToString(value[:])
		if _, collision := existing[id]; !collision {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique recovered connection ID")
}
