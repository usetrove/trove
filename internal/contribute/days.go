package contribute

import "time"

func parseDay(s string) (time.Time, error) { return time.Parse(time.DateOnly, s) }
