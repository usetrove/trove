# Launch drafts

These are drafts for you to review and edit. They contain no real numbers yet, because no benchmark or beta run has finished.

## The rule

Every figure (dollars, percentages, multipliers, token counts) must be written as a slot:

```
[MEASURED: what the number is — source: bench/results/<run>/summary.json]
[ESTIMATED: what the number is — source: bench/results/<run>/summary.json]
```

While the source still has a `<placeholder>`, the draft is unfinished. To fill a slot: run the benchmark or collect the beta data, commit the results file, replace the placeholder with the real path, and replace the slot text with the real number, keeping the label and path.

Check the drafts:

```sh
go run ./scripts/checkclaims              # draft mode: placeholders allowed, bare figures are not
go run ./scripts/checkclaims --publish    # release mode: every slot must cite an existing file
```

Label **MEASURED** only for counts from provider-reported usage or recorded observations. Dollar figures are **ESTIMATED** unless they come from an invoice (reconcile). Never round up, and never cite a number you cannot point to a file for.

## Files

- [data-post-outline.md](data-post-outline.md): "Where AI coding tokens actually go"
- [show-hn.md](show-hn.md)
- [x-thread.md](x-thread.md)
- [product-hunt.md](product-hunt.md)
- [demo-script.md](demo-script.md): 30 to 45 seconds
