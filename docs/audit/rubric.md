# Audit rubric

Every audit agent and the verifier judge the code against this list. It is distilled from primary sources, cited inline by key:

- **CRC**: [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- **GSG**: [Google Go Style Guide](https://google.github.io/styleguide/go/), its decisions and best-practices pages
- **Uber**: [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- **100GM**: [100 Go Mistakes](https://100go.co/)
- **Cheney**: [Practical Go](https://dave.cheney.net/practical-go/presentations/qcon-china.html)
- **Ousterhout**: *A Philosophy of Software Design*
- **SWE**: [Software Engineering at Google](https://abseil.io/resources/swe-book)
- **BL**: AWS Builders' Library articles on [timeouts, retries and backoff](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/) and [idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/)

## Ground rules for this repo

- **Behaviour is preserved.** Importer behaviour stays the same; the only change is smarter payee, category and memo. No flags and no new merge or review flows.
- **No naming-convention heuristics** in matching or learning.
- **No tests.** Do not add test files or test runs. The gates are static checks, builds, `cmd/evaluate` replay, and live dry runs.
- **Simplicity outranks defensive machinery.** This system serves one person, three banks and one YNAB budget. YNAB makes an occasional duplicate easy to see and delete. A mechanism must justify its complexity against that cost.

## A. Necessity and simplicity (check this first)

1. **Every mechanism names the failure it prevents.** For each one, record how often that failure has actually happened and what it costs when it does. Remove anything whose complexity exceeds that cost. Ousterhout: complexity is incremental, and each "just in case" adds to it.
2. **Define errors out of existence.** An idempotent delete, an empty result that is not an error, and an upsert are all better than a branch plus a review state. (Ousterhout, Cheney)
3. **Deep modules.** A module's interface should be much smaller than its implementation. Flag pass-through layers, shallow wrappers, and variables threaded through many layers. (Ousterhout)
4. **Duplication is cheaper than the wrong abstraction.** Flag helpers that grew mode or boolean parameters to serve callers that have since diverged. (Sandi Metz)
5. **Clear beats clever.** No reflection, generics or concurrency without a demonstrated need. Keep pure logic separate from I/O. (Go Proverbs, Pike's rules, Hickey's "Simple Made Easy")
6. **Dead and compatibility code goes.** That covers retired paths, legacy record shapes once the data has drained, and finished one-shot migrations.

## B. Package design and naming

7. **Name packages for what they provide.** No `util`, `common`, `types`, `service` or `models`. ([Package names](https://go.dev/blog/package-names), GSG)
8. **No stutter and no `Get` prefixes.** Prefer `ynab.Client` to `ynab.YNABClient`. (CRC, GSG)
9. **Layout:** a thin `cmd/<x>/main.go`, with everything else under `internal/`. ([Module layout](https://go.dev/doc/modules/layout), [AWS Lambda Go handler](https://docs.aws.amazon.com/lambda/latest/dg/golang-handler.html))
10. **Split packages by knowledge, not by layer or pipeline step.** (Cheney, Ousterhout's "temporal decomposition")
11. **No mutable package globals.** No `init()` with side effects, no `os.Getenv` outside `main`, and exit in one place. (Uber)

## C. Errors

12. **Never drop an error with `_`.** (CRC, 100GM #53)
13. **Handle each error once:** log it or return it, not both. (Uber)
14. **Wrap with context.** Use `%w` only when callers need to inspect the cause; use `%v` at boundaries. ([Go 1.13 errors](https://go.dev/blog/go1.13-errors), GSG)
15. **Use `errors.Is`/`As` or `errors.AsType`** (Go 1.26). Never match error strings. Name sentinels `ErrX` and types `XError`. (GSG, Uber)
16. **Error strings** are lowercase, without trailing punctuation or "failed to". (CRC)
17. **Reserve panics for programmer errors.** No `log.Fatal` outside `main`, and never `return nil, nil`. (CRC, Uber)

## D. Context, timeouts and cancellation

18. **`ctx` is the first parameter** and is never stored in a struct. ([Context and structs](https://go.dev/blog/context-and-structs))
19. **Every outbound call has a deadline.** No `http.DefaultClient`, no zero `Timeout`, always `NewRequestWithContext`, always close the body, and reuse clients. ([net/http](https://pkg.go.dev/net/http#Client), 100GM #81)
20. **Budgets come from the Lambda deadline,** leaving headroom to persist state. Per-call timeout < Lambda timeout < Step Functions timeout. ([Lambda Go context](https://docs.aws.amazon.com/lambda/latest/dg/golang-context.html))
21. **Cleanup that must outlive cancellation** uses `context.WithoutCancel` plus a short timeout. (Go 1.21)
22. **Always `defer cancel()`.** Prefer `WithTimeoutCause`.

## E. Concurrency

23. **Every goroutine has a known exit and a waiter.** Nothing keeps running after the handler returns. (CRC, Uber)
24. **Use `errgroup.SetLimit` or `sync.WaitGroup.Go`** (Go 1.25), not hand-rolled `Add`/`Done` plus error channels.
25. **Channels are unbuffered or size 1** unless justified, and sync types are never copied. (Uber)

## F. Logging and observability

26. **One JSON `log/slog` logger, built in `main`.** No `log.Printf` or `fmt.Println`. ([slog](https://go.dev/blog/slog))
27. **Constant messages, data in attributes,** consistent key casing, shared fields via `With`.
28. **Redact by type with `slog.LogValuer`.** Never log credentials, cookies, raw bank responses or Chromium stderr.

## G. Interfaces and dependencies

29. **The consumer defines a small interface,** and producers return concrete types. (CRC, Go Proverbs)
30. **No interface without two implementations or a real seam.** (GSG, 100GM #5)
31. **Inject clients and the clock through constructors.** (Uber, 100GM #87)
32. **Every dependency justifies itself.** "A little copying is better than a little dependency." Prefer the standard library. (SWE ch. 21, Go Proverbs)
33. **Run `govulncheck` and stay on a current Go.** Pin tools with go.mod `tool` directives. ([Go security](https://go.dev/doc/security/best-practices))

## H. Distributed-systems robustness

34. **Retry at exactly one layer,** with capped exponential backoff, full jitter and a maximum attempt count. Step Functions `JitterStrategy` defaults to NONE. (BL)
35. **Retry only transient failures** (5xx, 429, timeouts, `Retry-After`) and only idempotent operations. (BL)
36. **Every mutating call carries a deterministic idempotency key.** For YNAB that is `import_id`, at most 36 characters. In DynamoDB, use conditional writes. (BL, [DynamoDB conditions](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.ConditionExpressions.html))
37. **A lease needs a fencing token checked by storage,** and must be released or expire on a crash. ([Kleppmann](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html))
38. **The handler boundary returns typed errors** that Step Functions `Retry`/`Catch` can tell apart. Every Task has `TimeoutSeconds`. ([SFN error handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html))
39. **Bound growth:** TTL on per-job and per-notice records, and incremental reads (`since_date`, `last_knowledge_of_server`).

## I. Tooling

40. **CI runs** `gofmt`/`goimports`, `go vet`, `golangci-lint`, `govulncheck`, `go mod tidy -diff` and `go fix -diff`.

## Go 1.20–1.26 features that delete code

| Ver | Feature | Replaces |
|---|---|---|
| 1.20 | `errors.Join`, `context.WithCancelCause`, `strings.CutPrefix` | multierror helpers, `HasPrefix`+`TrimPrefix` |
| 1.21 | `min`/`max`/`clear`, `slices`, `maps`, `cmp`, `slog`, `sync.OnceValue`, `context.WithoutCancel`/`AfterFunc` | clamp `if`s, sort helpers, `sync.Once` boilerplate |
| 1.22 | per-iteration loop variables, `range n`, `cmp.Or` | `x := x`, default-value `if` chains |
| 1.23 | iterators, `slices.Sorted(maps.Keys(m))`, `slices.Chunk`, garbage-collected timers | sort-keys loops, manual batching, timer drains |
| 1.24 | `omitzero`, `strings.SplitSeq`, go.mod `tool`, `os.Root` | pointer time fields, `tools.go` |
| 1.25 | `sync.WaitGroup.Go`, vet `waitgroup`/`hostport` | `Add(1)`/`defer Done()` |
| 1.26 | `new(expr)`, `errors.AsType`, `slog.NewMultiHandler`, `go fix` modernizers | `ptr[T]` helpers, `errors.As` target vars |

## golangci-lint v2

The P0 `.golangci.yml` enables:
- the standard set: errcheck, govet, ineffassign, staticcheck, unused;
- HTTP and context: bodyclose, noctx, contextcheck, containedctx;
- errors: errorlint, nilerr, nilnil, errname;
- logging and safety: sloglint, gosec, forcetypeassert, exhaustive, musttag;
- simplicity: iface, unparam, gocognit, nestif, gocritic, revive;
- modernization: modernize, exptostd, perfsprint, unconvert, misspell;
- dependencies: depguard, gomoddirectives;
- formatters: gofumpt and goimports.

It deliberately leaves out funlen and dupl, which punish deliberate duplication (item 4).
