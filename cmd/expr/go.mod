module github.com/deepnoodle-ai/expr/cmd/expr

go 1.26.1

require (
	github.com/deepnoodle-ai/expr v0.0.0
	github.com/deepnoodle-ai/wonton v0.0.0
)

require (
	github.com/alecthomas/chroma/v2 v2.20.0 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/mattn/go-runewidth v0.0.17 // indirect
	github.com/rivo/uniseg v0.2.0 // indirect
	github.com/yuin/goldmark v1.7.13 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/term v0.38.0 // indirect
)

replace github.com/deepnoodle-ai/expr => ../..

replace github.com/deepnoodle-ai/wonton => ../../../wonton
