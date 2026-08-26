package service

import "time"

// clockSeam is the one notion of "now" a service reads.
//
// It is embedded rather than repeated, because the elevation surface spans
// five types -- UserService grants and slides the window, APIAuthHandler
// restricts the CLI consent legs by it, OAuthHandler grants through its
// re-authentication leg, AdminUserService mints the same credential the
// consent legs do, and AuthService REPORTS the deadline to the client -- and
// the five must agree. Two of them grew their own identical copy of the
// field plus the nil-check method, and the rest grew none, so GetCurrentUser
// answered from the wall clock while the grant beside it answered from the
// seam. A test that moved one moved half the surface, and what it pinned was
// the disagreement.
//
// The rule is the WHOLE type, not the elevation path inside it. Every
// instant a service that embeds this seam mints or compares comes from
// now(): a file that routes one expression through the seam and reads the
// wall clock for its sibling two lines down has the same disagreement in a
// smaller place, and `nextResendAt` had exactly that -- four callers on the
// seam and a fifth on the wall clock, so the password sign-up and the
// passkey sign-up reported one field from two clocks. A type that carries no
// seam is out of scope; giving it one is a separate change.
//
// Nil means time.Now, so production wires nothing and a test writes one
// field.
type clockSeam struct {
	// Now is the seam. Every instant a service mints or compares comes from
	// now(), so a fake that moves the window forward moves the grant, the
	// predicate, the slide and the reported deadline together.
	Now func() time.Time
}

// now returns the service's notion of the current instant.
func (c *clockSeam) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
