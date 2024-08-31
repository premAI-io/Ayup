package main

import (
	_ "embed"

	"premai.io/Ayup/go/cmd/ay"
)

//go:embed version.txt
var version string

func main() {
	ay.Main(version)
}
