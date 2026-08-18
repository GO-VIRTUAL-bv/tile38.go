---
name: Bug report
about: Something behaves differently than documented
labels: bug
---

## What happened

<!-- Include the error, or the wrong value you got back. -->

## Expected

## Reproduction

```go
// The client code, ideally the builder chain involved.
```

## Environment

- Library version / commit:
- Go version:
- Tile38 version (`SERVER`) and image:

## Raw reply (if this looks like a parsing problem)

<!--
Client.Do sends a raw command and returns the decoded reply, which pins down
whether the bug is in building the command or in parsing the answer:

    v, err := c.Do(ctx, "NEARBY", "fleet", "POINTS", "POINT", 33.5, -115.5, 5000)
    fmt.Printf("%#v %v\n", v, err)
-->
