package interceptor

import "time"

// SetClock sets the clock the client's identity entries age by (tests).
func SetClock(c *Client, now func() time.Time) { c.now = now }
