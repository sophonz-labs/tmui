# vt: charmbracelet/x/vt, patched

A copy of `github.com/charmbracelet/x/vt` v0.1.0 (MIT, `LICENSE` here, copyright Charmbracelet, Inc.). It is an ordinary package of this module, so everything that imports `pkg/vterm` gets the patch without a `replace` of its own. Only the non-test sources are here, and the package name is unchanged.

One change, in `utf8.go` `handleGrapheme`, marked `sophonz:`:

- a wide character that does not fit before the right margin now wraps to the next line first, as xterm does; v0.1.0 dropped it;
- a wide character that ends on the last column now leaves the cursor in the pending-wrap state; v0.1.0 moved it onto that column, so the next wide character overwrote it.

tmux draws a pane at the right edge with the terminal's own wrapping. Without this fix, Hangul and other wide text lost characters in the workspace. `pkg/vterm` tests both cases (`wide_test.go`, and `tmux_test.go` against a real tmux). Drop this copy once upstream has the fix.
