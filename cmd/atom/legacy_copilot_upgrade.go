//go:build legacy_copilot_upgrade

package main

// Keep these modules available for one migration release. Atom 0.2 generated
// ignored bundle files that survive its first git pull and import both packages.
import (
	_ "github.com/github/copilot-sdk/go/embeddedcli"
	_ "github.com/klauspost/compress/zstd"
)
