## benchgate

`origin/main (333333333333)` → `HEAD (444444444444)`

`example.com/m`

linux/amd64

| Benchmark | Unit | Base | Head | Delta | p | n | Verdict |
| --- | --- | --: | --: | --: | --: | --: | --- |
| `BenchmarkTiny-8` | ns/op | 100.0n | 130.0n | +30.00% | 0.100 | 3 | ⚠️ unmeasurable |
| `BenchmarkNoisy-8` | ns/op | 265.0n ± 240% | 277.5n ± 242% | +4.72% | 0.721 | 8 | ~ unchanged |

<details><summary>Warnings (4)</summary>

- `BenchmarkTiny-8 ns/op`: need >= 4 samples to detect a difference at alpha level 0.05
- `BenchmarkTiny-8 ns/op`: need >= 6 samples for confidence interval at level 0.95
- `BenchmarkNoisy-8 ns/op`: the base measurement varied by ±240%, well over ±10%; raise --rounds or --benchtime before trusting this verdict
- `BenchmarkNoisy-8 ns/op`: the head measurement varied by ±242%, well over ±10%; raise --rounds or --benchtime before trusting this verdict

</details>

**0 regressed**, 0 improved, 1 unchanged, 1 unmeasurable, 0 added, 0 removed.
