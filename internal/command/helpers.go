package command

import (
	"fmt"
	"time"

	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/state"
)

// requireConfig returns a non-nil *state.SafeConfig or an error.
func requireConfig(h *CommandHandler) (*state.SafeConfig, error) {
	if h == nil || h.config == nil {
		return nil, fmt.Errorf("config not loaded")
	}
	return h.config, nil
}

// isMod checks if a user is a moderator or the broadcaster.
func isMod(user chat.User) bool {
	if user.IsMod {
		return true
	}
	for _, badge := range []string{"broadcaster", "moderator", "lead_moderator"} {
		if _, ok := user.Badges[badge]; ok {
			return true
		}
	}
	return false
}

// pluralize adds an "s" to the end of a word if the count is not 1.
func pluralize(n int, toPluralize string) string {
	if n != 1 {
		toPluralize += "s"
	}
	return toPluralize
}

// formatDuration formats a time.Duration into a human-readable string
// such as "2 years and 3 months" or "45 seconds". The script package calls
// back into it via Env.FormatDuration so scripted commands render
// durations correctly.
func formatDuration(d time.Duration) string {
	type pointerIntPair struct {
		pointer *int
		n       int
	}

	d = d.Round(time.Second)
	var parts []string

	now := time.Now().UTC()
	since := now.Add(-d)

	y1, m1, d1 := now.Date()
	y2, m2, d2 := since.Date()

	years := int(y1 - y2)
	months := int(m1 - m2)
	days := int(d1 - d2)
	hours := now.Hour() - since.Hour()
	minutes := now.Minute() - since.Minute()
	seconds := now.Second() - since.Second()

	timespanList := [6]pointerIntPair{
		{&seconds, 60},
		{&minutes, 60},
		{&hours, 24},
		{&days, time.Date(y2, m2+1, 0, 0, 0, 0, 0, time.UTC).Day()},
		{&months, 12},
		{&years, 0},
	}

	for i := 0; i < len(timespanList)-1; i++ {
		if *timespanList[i].pointer < 0 {
			*timespanList[i+1].pointer--
			*timespanList[i].pointer += timespanList[i].n
		}
	}

	if years > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", years, pluralize(years, "year")))
	}
	if months > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", months, pluralize(months, "month")))
	}
	if days > 0 && len(parts) < 2 {
		parts = append(parts, fmt.Sprintf("%d %s", days, pluralize(days, "day")))
	}
	if hours > 0 && len(parts) < 2 {
		parts = append(parts, fmt.Sprintf("%d %s", hours, pluralize(hours, "hour")))
	}
	if minutes > 0 && len(parts) < 2 {
		parts = append(parts, fmt.Sprintf("%d %s", minutes, pluralize(minutes, "minute")))
	}
	if len(parts) < 2 {
		parts = append(parts, fmt.Sprintf("%d %s", seconds, pluralize(seconds, "second")))
	}

	out := parts[0]
	for _, p := range parts[1:] {
		out += " and " + p
	}
	return out
}
