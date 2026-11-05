# Jev assessment for Transactions — 5 November 2026

Jev is a plausible replacement for the semantic **payee selection** step. It is not a remedy for the current authentication, browser licensing, transaction identity, or notification transport failures. Keep the deterministic reconciliation and financial write controls authoritative.

## Where it fits

The current `internal/service/ai.go` uses a structured-output language model twice: to extract a merchant and amount from an email, and to choose an existing YNAB payee or invent a new name. Exact payee matches already bypass inference; JSONLogic overrides already express user decisions deterministically.

Jev's Choice primitive selects from supplied options and returns their probabilities. That fits choosing an existing payee without generating an arbitrary name. Use stable payee IDs as option keys, readable names as descriptions, and a companion Noul question asking whether any supplied payee actually fits. Its Choice limit is 255 options, so larger payee sets need deterministic candidate retrieval or a hierarchy. Returning a valid option does not establish that it is the correct payee.

A second reasonable use is suggesting a category or prioritizing a review queue. These should remain recommendations initially. Authentication status and safe retry eligibility already have explicit protocol evidence and typed failures; model inference adds little there.

## Where it does not fit

Do not let Jev decide whether two bank references identify one transaction, whether a deleted transaction should be restored, whether an uncertain write should be repeated, or whether to clear a financial hold. The RTC incident came from a deterministic identity collision. A probabilistic judgment cannot replace the bank references and YNAB audit needed to repair it.

Jev is not a direct replacement for arbitrary merchant-string and amount extraction. Its core primitives choose, score, or evaluate supplied propositions. Extraction can be built by parsing candidates in code and asking Jev which candidate applies, as TypeSafe's own extraction cookbook describes. Amounts, dates, signs and currencies should still be parsed and validated in code. Preserve exact billing descriptors needed by rules, such as APPLE.COM/BILL.

## What the documentation establishes

As of this review, TypeSafe lists `jev-1.14.0`, with a 64k per-request context and 32k per-question context. The HTTP API is `POST /v1/systemone`; the Go application can use its own bounded HTTP client. Pricing is $0.042 per million input tokens, with output free. For illustration, 1,000 requests of 2,000 input tokens would cost approximately $0.084, excluding other service charges. This application's low volume makes accuracy and maintenance more important than that potential saving.

Choice confidence is calculated from the largest option probability and the number of options. It is not an independent correctness check. For automatic selection, evaluate the chosen option's probability, the runner-up, and performance on this application's own labeled examples. Pin the evaluated model version because the `jev-latest` alias can change.

TypeSafe says it does not train on customer requests or responses. Its documentation identifies enterprise zero data retention separately; do not assume the ordinary tier retains nothing. No bank data or credentials were sent to TypeSafe during this assessment.

## Recommended evaluation

Keep exact matching and explicit overrides first. Evaluate Jev only for the existing semantic fallback, alongside the current model, using a small reviewed and minimized set of real descriptors and expected payees. Measure false matches, abstentions, latency and failures. Include confusing names, repeated purchases, payment processors, unknown merchants and billing suffixes. An abstention or timeout should retain the original descriptor or open a review, rather than select a guessed payee.

A model integration should be a separate change after the current incidents are repaired. No new model dependency, provider account, key or production decision path was introduced in this review.

Sources: [Introduction](https://docs.typesafe.ai/introduction), [Choice](https://docs.typesafe.ai/primitives/choice), [Confidence](https://docs.typesafe.ai/confidence), [Models, pricing and data handling](https://docs.typesafe.ai/models), [Candidate extraction cookbook](https://docs.typesafe.ai/cookbooks/pre_parsed_value_extraction_cookbook).
