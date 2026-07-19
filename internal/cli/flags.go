package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// sizeFlag parses a terminal size written the way people say it out loud,
// "80x24", rather than as two separate flags.
type sizeFlag struct {
	cols, rows int
}

func (s *sizeFlag) String() string {
	if s == nil || s.cols == 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", s.cols, s.rows)
}

func (s *sizeFlag) Set(v string) error {
	cols, rows, ok := strings.Cut(strings.ToLower(v), "x")
	if !ok {
		return fmt.Errorf("size %q must be COLSxROWS, such as 80x24", v)
	}
	c, err := strconv.Atoi(strings.TrimSpace(cols))
	if err != nil {
		return fmt.Errorf("size %q: columns must be an integer", v)
	}
	r, err := strconv.Atoi(strings.TrimSpace(rows))
	if err != nil {
		return fmt.Errorf("size %q: rows must be an integer", v)
	}
	if c <= 0 || r <= 0 {
		return fmt.Errorf("size %q: columns and rows must be positive", v)
	}
	s.cols, s.rows = c, r
	return nil
}

// envFlag collects repeated -env KEY=VALUE options.
type envFlag []string

func (e *envFlag) String() string { return strings.Join(*e, ",") }

func (e *envFlag) Set(v string) error {
	k, _, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("env %q must be KEY=VALUE", v)
	}
	if k == "" {
		return fmt.Errorf("env %q has an empty key", v)
	}
	*e = append(*e, v)
	return nil
}
