package bundler

import "errors"

// Classified failure modes a caller can branch on.
//
// Sentinels rather than string matching: orbital turns these into a metric
// label, and a label derived from an error message silently reclassifies
// itself the day someone rewords the message. These say what ORBITAL OBSERVED
// — whether the bundler answered at all — which is all it can know.
//
// ⚠️ Orbital cannot see WHY the bundler failed. A bundler that could not mint
// its own token answers 500, so it arrives here as ErrRejected, not as an auth
// failure. Distinguishing that needs the bundler to signal it structurally;
// reading its prose would be the string matching this exists to avoid.
var (
	// ErrUnreachable — the request never got an answer: DNS, connection
	// refused, timeout.
	ErrUnreachable = errors.New("bundler unreachable")
	// ErrRejected — the bundler answered, with a non-2xx.
	ErrRejected = errors.New("bundler rejected the request")
)

// classified carries a sentinel alongside the real error WITHOUT changing the
// message. The text is shown to operators in the UI and names the actual
// problem; prefixing it with a category would bury the detail that matters.
type classified struct {
	class error
	err   error
}

func (e classified) Error() string    { return e.err.Error() }
func (e classified) Unwrap() []error  { return []error{e.class, e.err} }
func classify(class, err error) error { return classified{class: class, err: err} }

// ClassifyForTest builds a classified error from outside the package so tests
// elsewhere can exercise a caller's branching without reaching into the
// client's HTTP path.
func ClassifyForTest(class, err error) error { return classify(class, err) }
