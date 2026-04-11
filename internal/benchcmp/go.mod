module github.com/deepnoodle-ai/expr/internal/benchcmp

go 1.26.1

require (
	github.com/deepnoodle-ai/expr v0.0.0
	github.com/expr-lang/expr v1.16.9
	github.com/google/cel-go v0.28.0
)

require (
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace github.com/deepnoodle-ai/expr => ../..
