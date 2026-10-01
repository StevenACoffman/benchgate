## benchgate

`origin/main (111111111111)` → `HEAD (222222222222)`

`example.com/m`

linux/amd64 · Test CPU

```
go test -run ^$ -bench . -benchmem -count 1 ./...
```

| Benchmark | Unit | Base | Head | Delta | p | n | Verdict |
| --- | --- | --: | --: | --: | --: | --: | --- |
| `BenchmarkSlower-8` | ns/op | 100.0n ± 1% | 130.0n ± 1% | +30.00% | <0.001 | 10 | 🔴 regressed |
| `BenchmarkNew-8` | ns/op | - | 100.0n ± 1% | - | - | 10 | ➕ added |
| `BenchmarkGone-8` | ns/op | 100.0n ± 1% | - | - | - | 10 | ➖ removed |

<details><summary>Benchmark coverage gaps (5)</summary>

- benchmarks execute 45.5% of statements, below the 80.0% floor
- example.com/m/nobench: package declares no Benchmark function
- example.com/m/sub: package declares no Benchmark function
- example.com/m/a.go:5: Cold: no benchmark executes any statement in this function
- example.com/m/a.go:7: Never: no benchmark executes any statement in this function

</details>

Benchmark statement coverage: **45.5%**

**1 regressed**, 0 improved, 0 unchanged, 0 unmeasurable, 1 added, 1 removed.

This run fails the gate (exit 2).
