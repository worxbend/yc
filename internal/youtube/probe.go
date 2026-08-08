package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ProbeTimeout bounds the reachability probe. The probe is run when something
// is already wrong, so it has to fail fast rather than hang a diagnostic.
const ProbeTimeout = 5 * time.Second

// ProbeReachability reports whether the API endpoint answers at all. It sends
// an unauthenticated HEAD, spends no quota, and carries no credential.
//
// Any HTTP response counts as reachable, including the 403 an unauthenticated
// caller gets: the question this answers is whether the network path exists,
// not whether the credential works. Those are different failures and telling
// them apart is the whole point of the check.
//
// It lives in this package rather than beside the doctor report because
// internal/app owns no network transport - that boundary is what keeps the UI
// lane free of I/O - and because the redaction rule this probe has to obey is
// this package's rule: no error may quote a request URL.
func ProbeReachability(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodHead, DefaultEndpoint+"/videos", nil)
	if err != nil {
		return errors.New("could not build the reachability probe request")
	}
	response, err := NewAPIHTTPClient(ProbeTimeout).Do(request)
	if err != nil {
		// The endpoint carries no credential today, but net/http echoes the
		// URL in its error text and an operator-supplied endpoint override
		// might not be as harmless. The reason is reported without it.
		return fmt.Errorf("no response from the youtube api: %w", probeCause(err))
	}
	defer response.Body.Close()
	return nil
}

// probeCause reduces a transport error to its class, so nothing derived from a
// request URL reaches the report.
func probeCause(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(err.Error(), "context deadline exceeded"):
		return fmt.Errorf("timed out after %s", ProbeTimeout)
	case strings.Contains(err.Error(), "no such host"):
		return errors.New("dns lookup failed")
	default:
		return errors.New("network error")
	}
}
