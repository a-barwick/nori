package conversation

import "time"

// Clock is the user's time zone, which the outer model needs to read "tomorrow" or "at ten".
type Clock struct {
	Location *time.Location
}

func (c Clock) location() *time.Location {
	if c.Location == nil {
		return time.UTC
	}
	return c.Location
}

func (c Clock) timeContext() string {
	location := c.location()
	return time.Now().In(location).Format("Monday, January 2, 2006 15:04 MST") + " (" + location.String() + ")"
}
