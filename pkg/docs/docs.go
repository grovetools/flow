package docs

import (
	_ "embed"
)

//go:embed docs.json
var DocsJSON []byte
