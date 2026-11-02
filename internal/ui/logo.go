package ui

import "os"

// logo is tmui's name, top right of the header as k9s puts its own.
var logo = []string{
	`   /$$                             /$$`,
	`  | $$                            |__/`,
	` /$$$$$$   /$$$$$$/$$$$  /$$   /$$ /$$`,
	`|_  $$_/  | $$_  $$_  $$| $$  | $$| $$`,
	`  | $$    | $$ \ $$ \ $$| $$  | $$| $$`,
	`  | $$ /$$| $$ | $$ | $$| $$  | $$| $$`,
	`  |  $$$$/| $$ | $$ | $$|  $$$$$$/| $$`,
	`   \___/  |__/ |__/ |__/ \______/ |__/`,
}

const (
	logoWidth = 38
	logoName  = "tmui" // the one-row logo, when the full one does not fit
	// logoMin is the narrowest terminal with the full logo: the server
	// column (38), two columns of keys (36) and the logo, with gaps.
	logoMin = 38 + 2 + 2*18 + 2 + logoWidth
	// logoTall is the shortest terminal that gives the header the logo's
	// rows; below it the table needs them more.
	logoTall = 30
)

// fullLogo is whether a w by h terminal shows the whole logo.
func fullLogo(w, h int) bool { return w >= logoMin && h >= logoTall }

// logoLines is the logo for a header of w columns and rows rows: the
// full one when it fits, else the name on the top row when room is left
// for a column of keys (cols is the space after the left column), else
// nil. Every line is as wide as the first. NO_COLOR gets plain text.
func logoLines(w, rows, cols int) []string {
	c := cOrange + bold
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		c = ""
	}
	src := logo
	if w < logoMin || rows < len(logo) {
		if cols-2-len(logoName) < 18 {
			return nil
		}
		src = []string{logoName}
	}
	out := make([]string, len(src))
	for i, l := range src {
		out[i] = c + fit(l, Cells(src[0])) + reset
	}
	return out
}
