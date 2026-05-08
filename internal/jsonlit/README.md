# jsonlit

Pre-parse rewrite that turns bare JSON-style array and brace literals into Go composite-literal syntax that `go/parser.ParseExpr` accepts.

```
[a, b, c]   ->  []any{a, b, c}
{"k": v}    ->  map[string]any{"k": v}
[]          ->  []any{}
{}          ->  map[string]any{}
```

The result is fed straight into `parser.ParseExpr`. jsonlit itself does no validation: anything it cannot classify unambiguously is left alone for the parser to reject.

## Approach

Token-based rewrite, no AST. We run `go/scanner` over the source, filter out comments and auto-inserted semicolons, and walk the token stream once. Edits are byte-range splices into the original source, so whitespace, formatting, and string contents are preserved verbatim. Strings, runes, and comments are scanner tokens we never look inside, so brackets and braces inside `"[1,2]"` or `// {x:1}` survive unchanged.

When the source contains neither `[` nor `{`, `Rewrite` returns it without invoking the scanner at all.

## Brackets

Each `[` falls into one of four roles. We disambiguate at the open by looking at the previous significant token, the contents of the bracket pair, and the token following the matching `]`. The matching `]` is precomputed in one pass via `matchBrackets`.

| Role         | Trigger                                                                    | Edit                        |
| ------------ | -------------------------------------------------------------------------- | --------------------------- |
| `frameEmpty` | `[]` not followed by a type-start                                          | `[]any{}`                   |
| `frameSlice` | `[]` followed by a type-start (IDENT, `[`, `map`, `interface`, `<-`, ...)  | leave alone (slice prefix)  |
| `frameIndex` | `[` after a value-producing token, or `[N]<type>`                          | leave alone (index or [N]T) |
| `frameArray` | everything else                                                            | rewrite to `[]any{ ... }`   |

The trickiest disambiguation is `[N]<rest>`. `[1][0]` is "literal then index"; `[3]int{1,2,3}` is a Go array type. We classify as an array type only when the bracket pair has no top-level comma and the token after the matching `]` starts a type expression. That type-start check is recursive: `[2][]int{{1},{2}}` is an array-of-slice type because the inner `[]` is itself a type prefix, and the chain extends as deep as you like (`[2][3][]int{}` works the same way). Without the recursion the rewrite emitted `[]any{2}[]int{...}`, which is not valid Go.

## Braces

Each `{` is either a bare object literal we should rewrite to `map[string]any{...}`, or a typed composite/function-literal body we must leave alone. The basic rule: if the previous token could end a type expression or close a function-signature paren (IDENT, `}`, `)`, `map`, `struct`, `interface`, `chan`, `func`), leave it alone; otherwise rewrite.

`)` is in that set because of `func() {}`, and `]` is in it because of generic instantiations like `List[int]{}`. In expression position `]{` only legitimately occurs after a type, since `arr[i]{...}` is not a valid Go expression.

### Type-omitted nested composites

Go lets you omit the inner type when a composite element type is itself composite: in `[][]int{{1,2},{3,4}}`, `{1,2}` inherits `[]int`. A naive "rewrite every brace whose previous token isn't type-like" mangles those inner braces into `map[string]any{1,2}`, which type-checks like nonsense.

We track a brace stack with three states:

- `braceObject`: a bare `{...}` we rewrote.
- `braceAny`: a typed composite whose element/value type is exactly `any` or `interface{}`, meaning `[]any`, `map[string]any`, `[]interface{}`, or `map[string]interface{}`.
- `braceInert`: any other typed composite (`[]struct{X int}`, `[][]int`, `[2][]int`, ...).

Inside `braceInert`, a nested `{` directly after `{`, `,`, or `:` is left alone, because it is a type-omitted composite that Go resolves from the outer type. Inside `braceAny` or `braceObject`, nested bare `{...}` keeps getting rewritten, which is the JSON-extension behavior real users rely on (`map[string]any{"k": {"j": 1}}` round-trips cleanly).

The `braceAny` carve-out is the reason `elementIsAnyOrInterface` exists rather than a simple "is the previous token type-like" check. `[]any` and friends are exactly the cases where Go does *not* permit type omission, so we must keep doing the rewrite. For `[][]any`, the element type is `[]any` (composite), so type omission applies and we should not rewrite; hence the `typeWraps` guard that rejects `[]any` when it sits inside another bracket pair.

## Things jsonlit does not try to do

- Validate the input. Malformed expressions get rewritten on a best-effort basis and the parser produces the error.
- Round-trip every valid Go expression semantically. Some Go shapes that the evaluator never accepts (e.g. `[2]any{1, 2}` as a fixed-length array of any, or `[3][]any{...}`) are classified pessimistically. The goal is correctness on idiomatic JSON-extended expressions, and never invalidating an input that real users write.
- Resolve type identity from token shape. `[]MyInterface{{"k": 1}}` is ambiguous: from the token stream alone we cannot tell whether `MyInterface` is a struct or an interface. We treat it as `braceInert` (struct-like, leave the inner brace alone), which is correct for struct-named types and produces invalid output for interface-named types. Real expr usage is `[]any` or `map[string]any` only, and those are handled by the `braceAny` carve-out.

## Known limitations

- **Comments inside slice/map type prefixes are dropped.** A comment between `[` and `]` of an empty array literal is preserved (`[/* x */]` becomes `[]any{/* x */}`), but a comment inside a slice-type prefix like `[]/* x */int{}` is left wherever the scanner places it; we do not try to reflow it.
- **Channel-only support is best-effort.** `[]chan T{}`, `[]<-chan T{}`, `[]chan<- T{}` all pass through, but the expr evaluator does not accept channel types regardless. Don't use them.
- **`[N]any{...}` (fixed-length array of any) is treated as inert.** Use `[]any{...}` instead. The expr evaluator does not accept fixed-length arrays.
- **Generics are recognized only via the `]` prefix rule.** Multi-parameter generics like `M[K, V]{}` work because `]` ends the type, but the rewriter does not attempt to validate that the bracketed content is a type list. A nonsense input like `M[1, 2]{}` will pass through and be rejected later.

## Tests

`TestRewrite` enumerates expected input/output pairs for every classification rule. `TestRewriteParses` asserts the rewritten output parses with `go/parser`. `TestIdempotent` asserts a second `Rewrite` is a no-op. `FuzzRewrite` enforces the round-trip invariant: any input that `parser.ParseExpr` accepted must still parse after rewriting. Fuzz seeds cover the classifications above plus shapes that previously broke the rewrite (`[2][]int{{1},{2}}`, `func() {}`, `[][]int{{1,2}}`, `[]<-chan int{}`, `map[string]any{"k": {"j": 1}}`).
