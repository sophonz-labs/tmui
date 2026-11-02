package ui

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
	// rows: 8 for the header and 6 for the prompt, frame, column names and
	// footer leave 6 rows of table, k9s's own minimum.
	logoTall = 20
)

// fullLogo is whether a w by h terminal has room for the whole logo.
func fullLogo(w, h int) bool { return w >= logoMin && h >= logoTall }

// logoLines is the logo for a header of w columns and rows rows: the
// full one when it fits, else the name on the top row when room is left
// for a column of keys (cols is the space after the left column), else
// nil. Every line is as wide as the first. NO_COLOR gets plain text.
func logoLines(w, rows, cols int) []string {
	c := cOrange + bold
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
