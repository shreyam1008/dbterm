package osservice

import "fmt"

type elevationError struct {
	operation string
	scope     Scope
	detail    string
}

func (e *elevationError) Error() string {
	detail := e.detail
	if detail == "" {
		detail = "retry from an elevated administrator or root session"
	}
	return fmt.Sprintf("%s for %s backup service: %v; %s", e.operation, e.scope, ErrElevationRequired, detail)
}

func (e *elevationError) Unwrap() error {
	return ErrElevationRequired
}

type elevationProbe func() (bool, error)

func requireElevation(scope Scope, operation string, probe elevationProbe) error {
	if scope != ScopeSystem {
		return nil
	}
	if probe == nil {
		probe = platformIsElevated
	}
	elevated, err := probe()
	if err != nil {
		return fmt.Errorf("check privileges before %s: %w", operation, err)
	}
	if elevated {
		return nil
	}
	return &elevationError{operation: operation, scope: scope}
}
