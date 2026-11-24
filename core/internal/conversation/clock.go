package conversation

import "time"

// Clock is the user's time zone, which the outer model needs to read "tomorrow" or "at ten".
type Clock struct {
	Location *time.Location
	// Now returns the current time for prompt context. Nil uses time.Now.
	Now func() time.Time
}

func (c Clock) location() *time.Location {
	if c.Location == nil {
		return time.UTC
	}
	return c.Location
}

func (c Clock) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c Clock) timeContext() string {
	location := c.location()
	return c.now().In(location).Format("Monday, January 2, 2006 15:04 MST") + " (" + location.String() + ")"
}
