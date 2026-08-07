# brutesubs

Streaming DNS subdomain candidate generation and resolution orchestration for large wordlists.

`brutesubs` provides a memory-efficient `subgen`-compatible stream and a batch workflow that:

1. Cleans and optionally deduplicates a wordlist.
2. Generates candidate subdomains for each root domain.
3. Resolves candidates with [`puredns`](https://github.com/d3mondev/puredns).
4. Removes intermediate candidate files after successful resolution.

The generator processes wordlists line by line instead of storing the complete input in memory, making it suitable for very large wordlists.

## Background

This project originated from a workflow built around [`subgen`](https://github.com/pry0cc/subgen) for candidate generation followed by [`puredns`](https://github.com/d3mondev/puredns) resolution using trusted public resolver lists. That workflow is useful, but holding very large wordlists in memory can make the original generator impractical. In addition, `puredns bruteforce` is not always as fast or reliable as a dedicated streaming generation stage for large wordlists and may encounter errors depending on the input and environment.

`brutesubs` keeps the same useful pipeline while separating the stages: stream candidate generation with bounded memory, then resolve the generated candidates with `puredns`. The batch mode also removes completed intermediate files so large candidate lists do not accumulate on disk.

## Features

- Streaming stdin-to-stdout candidate generation.
- Compatible with the `subgen -d DOMAIN` interface.
- Batch processing for a file containing multiple root domains.
- Lowercasing and DNS-safe wordlist cleaning.
- Optional deduplication using external sorting.
- Chunked `puredns` execution to limit resource usage.
- Safe intermediate-file cleanup with retry-friendly failure handling.
- Pure Go implementation with no third-party Go dependencies.

## Requirements

- Go 1.22 or newer for building from source.
- [`puredns`](https://github.com/d3mondev/puredns) available in `PATH` for resolution workflows.
- GNU `sort` is recommended when deduplicating large wordlists.

## Installation

### Install with Go

```bash
go install github.com/abhinavskjha/brutesubs@latest
```

The binary is installed as `brutesubs` in the configured Go binary directory.

### Build from source

```bash
git clone https://github.com/abhinavskjha/brutesubs.git
cd brutesubs
go build -trimpath -ldflags="-s -w" -o brutesubs .
```

## Usage

### Streaming mode

Use `-d` to read words from standard input and write generated candidates to standard output:

```bash
cat wordlist.txt | brutesubs -d example.com
```

The explicit form is also supported:

```bash
cat wordlist.txt | brutesubs --subgen-mode -d example.com
```

For resolution through `puredns`:

```bash
cat wordlist.txt | brutesubs -d example.com | puredns resolve
```

Streaming mode:

- Processes one input line at a time.
- Emits one candidate for each input line.
- Preserves duplicate input lines to maintain constant memory usage.
- Converts output to lowercase.
- Removes characters outside `a-z`, `0-9`, `-`, and `.`.
- Writes candidates only to stdout; diagnostic messages go to stderr.
- Does not invoke `puredns` itself.

### Batch generation and resolution

Create a roots file with one root domain per line:

```text
example.com
test.com
```

Run the complete workflow:

```bash
brutesubs \
  -roots ./roots \
  -wordlist ./wordlist.txt \
  -to-brute ./to-brute \
  -resolved ./resolved
```

The roots loader ignores blank lines, comments beginning with `#`, and duplicate entries. A leading `*.` is removed from wildcard roots.

### Large wordlists

Clean and deduplicate a large wordlist once:

```bash
brutesubs \
  -wordlist ./wordlists/large.txt \
  -cleaned .cache/large.cleaned.txt \
  -sort-mem 1G \
  -clean-only
```

Use the cached result for the batch workflow. `-no-dedup` avoids repeating deduplication when the cached wordlist is already prepared:

```bash
brutesubs \
  -roots ./roots \
  -wordlist ./wordlists/large.txt \
  -cleaned .cache/large.cleaned.txt \
  -no-dedup \
  -to-brute ./to-brute \
  -resolved ./resolved \
  -puredns-workers 1 \
  -chunk 500000
```

The cleaned wordlist is lowercased, filtered to DNS-safe characters, validated, and cached. GNU `sort -u` is used when available so deduplication remains disk-backed rather than requiring the entire unique set in memory.

### Separate workflow stages

Generate candidates without resolving them:

```bash
brutesubs \
  -roots ./roots \
  -wordlist ./wordlist.txt \
  -generate-only \
  -to-brute ./to-brute
```

Resolve previously generated candidates:

```bash
brutesubs \
  -roots ./roots \
  -resolve-only \
  -to-brute ./to-brute \
  -resolved ./resolved
```

## Intermediate-file cleanup

The batch workflow is designed not to retain multi-gigabyte candidate files unnecessarily:

- After a successful `puredns` run, the corresponding input file in `to-brute/` is deleted.
- If a non-empty resolved output already exists, its corresponding input is also removed.
- After every generated root resolves successfully, the complete `to-brute/` directory is removed.
- If any resolution fails, remaining candidate files and the directory are preserved for retry.
- `-generate-only` does not remove generated files because no resolution has completed.

## Command-line options

| Option | Description | Default |
| --- | --- | --- |
| `-roots` | File containing root domains | `./roots` |
| `-wordlist` | Raw wordlist to clean and process | `./wordlist` |
| `-cleaned` | Cached cleaned wordlist | `.cache/cleaned.wordlist` |
| `-to-brute` | Directory for generated candidates | `./to-brute` |
| `-resolved` | Directory for resolved domains | `./resolved` |
| `-sort-mem` | Memory limit passed to GNU `sort` | `1G` |
| `-chunk` | Maximum lines processed per resolver chunk | `500000` |
| `-puredns-workers` | Number of concurrent resolver jobs | `1` |
| `-no-dedup` | Skip wordlist deduplication | `false` |
| `-generate-only` | Stop after candidate generation | `false` |
| `-resolve-only` | Skip cleaning and generation | `false` |
| `-clean-only` | Clean the wordlist and exit | `false` |
| `-subgen-mode` | Enable stdin-to-stdout streaming mode | `false` |
| `-d` | Domain used by streaming mode; implies `-subgen-mode` | — |
| `-version` | Print version information | `false` |

## Development

Run the build and test targets:

```bash
make build
go test ./...
go vet ./...
go test -race ./...
make small-test
```

Remove local build and workflow artifacts:

```bash
make clean
```

## Credits and sources

`brutesubs` builds on the following projects, datasets, and resolver resources:

- **subgen** — the original candidate-generation idea and command-line workflow: [github.com/pry0cc/subgen](https://github.com/pry0cc/subgen)
- **puredns** — candidate resolution and DNS bruteforce tooling: [github.com/d3mondev/puredns](https://github.com/d3mondev/puredns)
- **Trickest subdomain bruteforce list** — the large all-subdomain wordlist used as a source for this workflow: [all.txt.zip](https://localdomain.pw/subdomain-bruteforce-list/all.txt.zip)
- **Trickest trusted resolvers** — curated trusted resolver list: [resolvers-trusted.txt](https://raw.githubusercontent.com/trickest/resolvers/main/resolvers-trusted.txt)
- **Trickest resolver list** — broader resolver list: [resolvers.txt](https://raw.githubusercontent.com/trickest/resolvers/main/resolvers.txt)
- **Assetnote best DNS wordlist** — a commonly used DNS discovery wordlist: [best-dns-wordlist.txt](https://wordlists-cdn.assetnote.io/data/manual/best-dns-wordlist.txt)
- **Assetnote wordlists** — automatically generated and maintained wordlists: [wordlists.assetnote.io](https://wordlists.assetnote.io/)

These resources are maintained by their respective authors and organizations. Review and comply with each source's license and usage terms.

## Authorized use

Use this tool only against domains and infrastructure you own or are explicitly authorized to assess. Follow applicable laws, service terms, and testing agreements.

## License

MIT License.
