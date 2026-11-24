module github.com/lkvapil/prusa-driver-prusalink

go 1.26.11

require github.com/prusa3d/prusa-connect/sdk v0.0.0

// The sdk module carries no published version, so a driver is built against the
// sdk directory of a prusa-connect checkout. Swap this for a require once
// the module is tagged.
replace github.com/prusa3d/prusa-connect/sdk => /Users/lukaskvapil/Documents/prusa-connect/sdk
