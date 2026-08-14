package cmd

import "fmt"

// This file implements the shared pagination flag contract of the get
// and view commands: the --offset/--limit/--page window. Both commands
// register the same flags with the same defaults; the validation rules
// and the effective-window math live here once.

// paginationFlags carries the retrieval pagination flags of one run:
// the raw values plus whether each flag was explicitly given (the
// default output — nothing given — stays byte-identical to the
// unpaged schema).
type paginationFlags struct {
	offset, limit, page int
	offsetGiven         bool
	limitGiven          bool
	pageGiven           bool
}

// validate checks the deterministic value and combination rules of the
// pagination flags (usage errors, exit 2): a GIVEN limit and page must
// be >= 1, a GIVEN offset >= 0, --page requires --limit, and --offset
// and --page are mutually exclusive. Flags that were not given keep
// their defaults (0/0/1) and are never validated — the default output
// stays byte-identical to the unpaged schema.
func (f paginationFlags) validate() error {
	switch {
	case f.limitGiven && f.limit < 1:
		return fmt.Errorf("--limit must be >= 1")
	case f.offsetGiven && f.offset < 0:
		return fmt.Errorf("--offset must be >= 0")
	case f.pageGiven && f.page < 1:
		return fmt.Errorf("--page must be >= 1")
	case f.pageGiven && !f.limitGiven:
		return fmt.Errorf("--page requires --limit")
	case f.offsetGiven && f.pageGiven:
		return fmt.Errorf("--offset and --page are mutually exclusive")
	}
	return nil
}

// apply computes the effective window once the total item count of the
// paged list is known; apply=false means no pagination at all (the
// byte-identical default). Rules:
//
//	--page given        offset = (page-1)*limit, limit = limit
//	--offset given only limit = total-offset (0 past the end)
//	--limit given only  offset = 0, limit = limit
func (f paginationFlags) apply(total int) (bool, int, int) {
	if !f.offsetGiven && !f.limitGiven && !f.pageGiven {
		return false, 0, 0
	}
	offset, limit := f.offset, f.limit
	switch {
	case f.pageGiven:
		offset = (f.page - 1) * f.limit
	case limit == 0:
		limit = total - offset
		if limit < 0 {
			limit = 0
		}
	}
	return true, offset, limit
}

// pageNumberOf returns the 1-based effective page of a window:
// offset/limit+1 when limit > 0, else 1.
func pageNumberOf(offset, limit int) int {
	if limit <= 0 {
		return 1
	}
	return offset/limit + 1
}
