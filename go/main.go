package main

import (
	_ "embed"

	"premai.io/Ayup/go/cmd/ay"
)

//go:embed version.txt
var version []byte

func main() {
	ay.Main(version)
}
