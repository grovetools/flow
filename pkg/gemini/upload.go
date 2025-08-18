package gemini

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"
)

// uploadFile uploads a single file to the Gemini API
func uploadFile(ctx context.Context, client *genai.Client, filePath string) (*genai.File, error) {
	uploadStart := time.Now()
	f, err := client.Files.UploadFromPath(
		ctx,
		filePath,
		&genai.UploadFileConfig{
			MIMEType: detectMIMEType(filePath),
		},
	)
	if err != nil {
		return nil, err
	}
	
	fmt.Fprintf(os.Stderr, "  ✅ %s (%.2fs)\n", filepath.Base(filePath), time.Since(uploadStart).Seconds())
	return f, nil
}

// detectMIMEType returns appropriate MIME type for a file
func detectMIMEType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".txt", ".text":
		return "text/plain"
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".go":
		return "text/x-go"
	case ".py":
		return "text/x-python"
	case ".js":
		return "text/javascript"
	case ".ts":
		return "text/x-typescript"
	case ".jsx":
		return "text/javascript"
	case ".tsx":
		return "text/x-typescript"
	case ".java":
		return "text/x-java"
	case ".c":
		return "text/x-c"
	case ".cpp", ".cc", ".cxx":
		return "text/x-c++"
	case ".h", ".hpp":
		return "text/x-c++"
	case ".cs":
		return "text/x-csharp"
	case ".php":
		return "text/x-php"
	case ".rb":
		return "text/x-ruby"
	case ".swift":
		return "text/x-swift"
	case ".kt":
		return "text/x-kotlin"
	case ".rs":
		return "text/x-rust"
	case ".scala":
		return "text/x-scala"
	case ".r":
		return "text/x-r"
	case ".m":
		return "text/x-objective-c"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".scss", ".sass":
		return "text/x-scss"
	case ".less":
		return "text/x-less"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "text/yaml"
	case ".toml":
		return "text/x-toml"
	case ".ini":
		return "text/x-ini"
	case ".sh", ".bash":
		return "text/x-shellscript"
	case ".bat", ".cmd":
		return "text/x-bat"
	case ".ps1":
		return "text/x-powershell"
	case ".sql":
		return "text/x-sql"
	case ".dockerfile":
		return "text/x-dockerfile"
	case ".makefile", ".mk":
		return "text/x-makefile"
	case ".gradle":
		return "text/x-gradle"
	case ".cmake":
		return "text/x-cmake"
	case ".proto":
		return "text/x-protobuf"
	case ".graphql", ".gql":
		return "text/x-graphql"
	case ".vue":
		return "text/x-vue"
	case ".svelte":
		return "text/x-svelte"
	case ".elm":
		return "text/x-elm"
	case ".clj", ".cljs":
		return "text/x-clojure"
	case ".dart":
		return "text/x-dart"
	case ".erl":
		return "text/x-erlang"
	case ".ex", ".exs":
		return "text/x-elixir"
	case ".lua":
		return "text/x-lua"
	case ".nim":
		return "text/x-nim"
	case ".zig":
		return "text/x-zig"
	case ".pl":
		return "text/x-perl"
	case ".rkt":
		return "text/x-racket"
	case ".ml", ".mli":
		return "text/x-ocaml"
	case ".fs", ".fsi", ".fsx":
		return "text/x-fsharp"
	case ".v":
		return "text/x-verilog"
	case ".vhd", ".vhdl":
		return "text/x-vhdl"
	case ".asm", ".s":
		return "text/x-asm"
	case ".tex":
		return "text/x-tex"
	case ".bib":
		return "text/x-bibtex"
	default:
		return "text/plain"
	}
}