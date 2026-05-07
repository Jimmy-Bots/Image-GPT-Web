package timeutil

import "time"

var shanghaiLocation = mustLoadLocation("Asia/Shanghai")

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}

func InShanghai(now time.Time) time.Time {
	return now.In(shanghaiLocation)
}

func ShanghaiDayString(now time.Time) string {
	return InShanghai(now).Format("2006-01-02")
}

