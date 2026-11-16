package services

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

const (
	InternalServiceSecretHeader = "X-Internal-Service-Secret"
	internalServiceSecretEnv    = "INTERNAL_SERVICE_SHARED_SECRET"
)

// SetInternalServiceAuthHeader attaches the shared secret used for backend -> internal service calls.
func SetInternalServiceAuthHeader(req *http.Request) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	secret := strings.TrimSpace(os.Getenv(internalServiceSecretEnv))
	if secret == "" {
		return fmt.Errorf("%s is not set", internalServiceSecretEnv)
	}

	req.Header.Set(InternalServiceSecretHeader, secret)
	return nil
}
