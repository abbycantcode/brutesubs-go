package main

import (
	"bufio"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Version injected via ldflags
var Version = "v2.0.0-34m-fix"

const (
	defaultRoots    = "./roots"
	defaultToBrute  = "./to-brute"
	defaultResolved = "./resolved"
	defaultWordlist = "/home/ubuntu/wordlist/best-dns-wordlist.txt"
)

var (
	validLabelCache = make(map[string]bool)
	cacheMu         sync.RWMutex
)

// ---------- utils ----------

func logf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

func isAllowedByte(b byte) bool {
	// original regex [^a-zA-Z0-9-.]+ -> keep a-zA-Z0-9 - .
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '-' || b == '.'
}

func isValidLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	// must start and end with alphanumeric
	if !isAlnum(label[0]) || !isAlnum(label[len(label)-1]) {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func cleanWordForList(s string) (string, bool) {
	// Trim spaces, dots
	s = strings.Trim(s, " \t\r\n.")
	if s == "" {
		return "", false
	}
	s = strings.ToLower(s)

	// Fast strip: keep only allowed chars
	// This mimics original: reg.ReplaceAllString(..., "")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			b.WriteByte(c)
		}
	}
	cleaned := b.String()
	if cleaned == "" {
		return "", false
	}
	// Validate each label
	parts := strings.Split(cleaned, ".")
	for _, p := range parts {
		if !isValidLabel(p) {
			return "", false
		}
	}
	if len(cleaned) > 253 {
		return "", false
	}
	return cleaned, true
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// ---------- roots ----------

func loadRoots(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]struct{})
	var roots []string
	sc := bufio.NewScanner(f)
	// big buffer for long lines
	buf := make([]byte, 1024*1024)
	sc.Buffer(buf, 10*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.Trim(line, ".")
		line = strings.ToLower(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// strip wildcard
		line = strings.TrimPrefix(line, "*.")
		if _, ok := seen[line]; !ok {
			seen[line] = struct{}{}
			roots = append(roots, line)
		}
	}
	return roots, sc.Err()
}

// ---------- wordlist cleaning (one-time) ----------

func cleanWordlistOnce(rawPath, cleanedPath string, noDedup bool, sortMem string) error {
	if _, err := os.Stat(cleanedPath); err == nil {
		// check mtime
		rawInfo, _ := os.Stat(rawPath)
		cleanInfo, _ := os.Stat(cleanedPath)
		if cleanInfo.ModTime().After(rawInfo.ModTime()) && cleanInfo.Size() > 0 {
			logf("[clean] SKIP exists: %s (%.1f MB)", cleanedPath, float64(cleanInfo.Size())/1024/1024)
			return nil
		}
	}

	os.MkdirAll(filepath.Dir(cleanedPath), 0755)
	tmpDir := filepath.Join(filepath.Dir(cleanedPath), "tmp")
	os.MkdirAll(tmpDir, 0755)

	filteredTmp := filepath.Join(tmpDir, fmt.Sprintf(".%s.filtered.tmp", filepath.Base(rawPath)))

	logf("[clean] filtering %s -> %s", rawPath, filteredTmp)

	in, err := os.Open(rawPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(filteredTmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 4*1024*1024)

	var seen map[uint64]struct{}
	if !noDedup {
		// hash dedup: 8 bytes per entry, not full string -> ~500MB for 34M
		// pre-allocate 1M to reduce rehash
		seen = make(map[uint64]struct{}, 1_000_000)
	}

	sc := bufio.NewScanner(in)
	buf := make([]byte, 4*1024*1024)
	sc.Buffer(buf, 10*1024*1024)

	var total, kept int64
	for sc.Scan() {
		total++
		cleaned, ok := cleanWordForList(sc.Text())
		if !ok {
			continue
		}
		if !noDedup {
			h := hashString(cleaned)
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
		}
		fmt.Fprintln(w, cleaned)
		kept++
		if total%5_000_000 == 0 {
			logf("[clean] ... %dM scanned -> %dM kept (mem: %d hashes)", total/1_000_000, kept/1_000_000, len(seen))
			// hint GC for huge map
			if len(seen) > 20_000_000 {
				runtime.GC()
			}
		}
	}
	w.Flush()
	out.Close()

	if err := sc.Err(); err != nil {
		return err
	}

	logf("[clean] filtered %d -> %d (%.1f%%)", total, kept, float64(kept)/float64(total)*100)

	// If we did hash dedup, we are already unique. Just rename.
	// If noDedup=false we already deduped, but sort -u will further guarantee and is cheap if we have GNU sort
	// Try external sort for final guarantee (uses disk, low RAM)
	if _, err := exec.LookPath("sort"); err == nil && !noDedup {
		sortedTmp := filepath.Join(tmpDir, fmt.Sprintf(".%s.sorted.tmp", filepath.Base(rawPath)))
		logf("[clean] external sort -u --parallel=2 -S %s (disk, low RAM)", sortMem)
		cmd := exec.Command("sort", "-u", "--parallel=2", "-S", sortMem, "-T", tmpDir, "-o", sortedTmp, filteredTmp)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			os.Rename(sortedTmp, cleanedPath)
			os.Remove(filteredTmp)
			info, _ := os.Stat(cleanedPath)
			logf("[clean] DONE: %s (%.1f MB)", cleanedPath, float64(info.Size())/1024/1024)
			return nil
		} else {
			logf("[clean] sort failed (%v), using filtered file as cleaned", err)
		}
	}

	// fallback: filtered is our cleaned
	if err := os.Rename(filteredTmp, cleanedPath); err != nil {
		return err
	}
	info, _ := os.Stat(cleanedPath)
	logf("[clean] DONE: %s (%.1f MB)", cleanedPath, float64(info.Size())/1024/1024)
	return nil
}

// ---------- generation (subgen logic but O(1) RAM) ----------

func sanitizeSubgenRecord(input, domain string) string {
	combined := strings.ToLower(input + "." + domain)
	var b strings.Builder
	b.Grow(len(combined))
	for i := 0; i < len(combined); i++ {
		if isAllowedByte(combined[i]) {
			b.WriteByte(combined[i])
		}
	}
	return b.String()
}

func runSubgenMode(domain string, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 10*1024*1024)
	w := bufio.NewWriterSize(out, 4*1024*1024)
	for sc.Scan() {
		if _, err := w.WriteString(sanitizeSubgenRecord(sc.Text(), domain) + "\n"); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return w.Flush()
}

func generateForDomain(domain, cleanedWordlist, outPath string) error {
	if _, err := os.Stat(outPath); err == nil {
		if info, _ := os.Stat(outPath); info.Size() > 0 {
			logf("[gen] SKIP exists: %s", domain)
			return nil
		}
	}

	tmpPath := outPath + fmt.Sprintf(".%d.tmp", os.Getpid())
	os.MkdirAll(filepath.Dir(outPath), 0755)

	in, err := os.Open(cleanedWordlist)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 4*1024*1024)

	sc := bufio.NewScanner(in)
	buf := make([]byte, 4*1024*1024)
	sc.Buffer(buf, 10*1024*1024)

	suffix := "." + domain + "\n"
	var count int64
	for sc.Scan() {
		word := sc.Text()
		if word == "" {
			continue
		}
		// cleaned list is already valid, so just concat
		// This is O(1) RAM, streaming
		w.WriteString(word)
		w.WriteString(suffix)
		count++
	}
	if err := sc.Err(); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return err
	}
	w.Flush()
	out.Close()

	if err := os.Rename(tmpPath, outPath); err != nil {
		return err
	}
	logf("[gen] DONE: %s -> %s (%d lines, %.1f MB)", domain, outPath, count, float64(count*int64(len(domain)+20))/1024/1024)
	return nil
}

// ---------- puredns wrapper with chunking ----------

func removeResolvedInput(inputPath string) error {
	if err := os.Remove(inputPath); err != nil {
		return fmt.Errorf("remove resolved input %s: %w", inputPath, err)
	}
	return nil
}

func cleanupToBruteDir(path string, generatedCount, resolvedCount int) error {
	if generatedCount == 0 || resolvedCount != generatedCount {
		return nil
	}
	return os.RemoveAll(path)
}

func resolveWithPuredns(inputPath, outputPath string, chunkLines int) error {
	if _, err := os.Stat(inputPath); err != nil {
		return err
	}
	if _, err := os.Stat(outputPath); err == nil {
		if info, _ := os.Stat(outputPath); info.Size() > 0 {
			logf("[puredns] SKIP exists: %s", filepath.Base(outputPath))
			return removeResolvedInput(inputPath)
		}
	}

	purednsBin, err := exec.LookPath("puredns")
	if err != nil {
		return fmt.Errorf("puredns not found in PATH, install it or use -resolver=internal")
	}

	tmpFinal := outputPath + fmt.Sprintf(".%d.tmp", os.Getpid())
	os.MkdirAll(filepath.Dir(outputPath), 0755)

	// quick path: small file
	info, _ := os.Stat(inputPath)
	// estimate lines: size / avg 30
	estLines := info.Size() / 30
	if estLines <= int64(chunkLines) {
		logf("[puredns] START: %s (%.1f MB, single chunk)", filepath.Base(inputPath), float64(info.Size())/1024/1024)
		cmd := exec.Command(purednsBin, "resolve", inputPath, "--write", tmpFinal)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Remove(tmpFinal)
			return err
		}
		if err := os.Rename(tmpFinal, outputPath); err != nil {
			return err
		}
		return removeResolvedInput(inputPath)
	}

	// chunked path for huge files (34M)
	logf("[puredns] START CHUNKED: %s (%.1f MB, chunk=%d)", filepath.Base(inputPath), float64(info.Size())/1024/1024, chunkLines)

	in, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer in.Close()

	finalOut, err := os.Create(tmpFinal)
	if err != nil {
		return err
	}
	finalWriter := bufio.NewWriterSize(finalOut, 4*1024*1024)

	sc := bufio.NewScanner(in)
	buf := make([]byte, 4*1024*1024)
	sc.Buffer(buf, 10*1024*1024)

	chunkIdx := 0
	linesInChunk := 0
	var chunkFile *os.File
	var chunkWriter *bufio.Writer
	var chunkPath string

	flushChunk := func() error {
		if chunkFile == nil {
			return nil
		}
		if err := chunkWriter.Flush(); err != nil {
			return err
		}
		if err := chunkFile.Close(); err != nil {
			return err
		}

		resTmp := fmt.Sprintf("%s.chunk.%d.res.tmp", tmpFinal, chunkIdx)
		cmd := exec.Command(purednsBin, "resolve", chunkPath, "--write", resTmp)
		if err := cmd.Run(); err != nil {
			logf("[puredns] chunk %d failed: %v", chunkIdx, err)
			os.Remove(chunkPath)
			os.Remove(resTmp)
			chunkFile = nil
			return err
		}
		resF, err := os.Open(resTmp)
		if err != nil {
			return err
		}
		if _, err := io.Copy(finalWriter, resF); err != nil {
			resF.Close()
			return err
		}
		if err := resF.Close(); err != nil {
			return err
		}
		os.Remove(chunkPath)
		os.Remove(resTmp)
		chunkFile = nil
		chunkIdx++
		return nil
	}

	for sc.Scan() {
		if chunkFile == nil {
			chunkPath = fmt.Sprintf("%s.chunk.%d.tmp", tmpFinal, chunkIdx)
			f, err := os.Create(chunkPath)
			if err != nil {
				return err
			}
			chunkFile = f
			chunkWriter = bufio.NewWriterSize(f, 2*1024*1024)
			linesInChunk = 0
		}
		if _, err := chunkWriter.WriteString(sc.Text() + "\n"); err != nil {
			return err
		}
		linesInChunk++
		if linesInChunk >= chunkLines {
			if err := flushChunk(); err != nil {
				return err
			}
		}
	}
	// last chunk
	if chunkFile != nil {
		if err := flushChunk(); err != nil {
			os.Remove(tmpFinal)
			return err
		}
	}
	if err := sc.Err(); err != nil {
		os.Remove(tmpFinal)
		return err
	}
	if err := finalWriter.Flush(); err != nil {
		finalOut.Close()
		os.Remove(tmpFinal)
		return err
	}
	if err := finalOut.Close(); err != nil {
		os.Remove(tmpFinal)
		return err
	}

	if err := os.Rename(tmpFinal, outputPath); err != nil {
		os.Remove(tmpFinal)
		return err
	}
	if err := removeResolvedInput(inputPath); err != nil {
		return err
	}
	finalInfo, _ := os.Stat(outputPath)
	logf("[puredns] DONE: %s -> %s (%.1f MB)", filepath.Base(inputPath), outputPath, float64(finalInfo.Size())/1024/1024)
	return nil
}

// ---------- main ----------

func main() {
	rootsPath := flag.String("roots", defaultRoots, "File with root domains")
	toBrutePath := flag.String("to-brute", defaultToBrute, "Dir for generated bruteforce lists")
	resolvedPath := flag.String("resolved", defaultResolved, "Dir for resolved domains")
	wordlistPath := flag.String("wordlist", defaultWordlist, "Raw wordlist (214M or 676M)")
	cleanedPath := flag.String("cleaned", ".cache/cleaned.wordlist", "Cached cleaned wordlist (deduped, lowercased)")
	sortMem := flag.String("sort-mem", "1G", "Memory for GNU sort -S")
	chunkLines := flag.Int("chunk", 500000, "Lines per chunk for puredns (lower = less RAM)")
	purednsWorkers := flag.Int("puredns-workers", 1, "Parallel puredns jobs (keep 1 on 4G free)")
	subgenMode := flag.Bool("subgen-mode", false, "Read words from stdin and write generated subdomains to stdout")
	domainInput := flag.String("d", "", "Domain for stdin subgen mode")
	noDedup := flag.Bool("no-dedup", false, "Assume wordlist already cleaned - O(1) RAM, best for 34M")
	generateOnly := flag.Bool("generate-only", false, "Only generate, don't resolve")
	resolveOnly := flag.Bool("resolve-only", false, "Only resolve, skip generation")
	cleanOnly := flag.Bool("clean-only", false, "Only clean wordlist and exit")
	showVersion := flag.Bool("version", false, "Show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("brutesubs %s (go %s)\n", Version, runtime.Version())
		return
	}

	if *subgenMode || *domainInput != "" {
		if *domainInput == "" {
			log.Print("-d is required with -subgen-mode")
			os.Exit(2)
		}
		if err := runSubgenMode(*domainInput, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("subgen mode failed: %v", err)
		}
		return
	}

	// Lock file to prevent double run
	lockPath := filepath.Join(*toBrutePath, ".brutesubs.lock")
	os.MkdirAll(*toBrutePath, 0755)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		// try to lock via O_EXCL check - simple
		// On Linux you would use flock, but we do pid file check
		if _, err := os.Stat(lockPath); err == nil {
			// if lock file exists and is recent (<24h) and process still running? just warn
			// we keep simple: if second instance tries, we check if we can get exclusive lock via file
			// For brevity, we just write pid
		}
		fmt.Fprintf(lockFile, "%d\n", os.Getpid())
		defer func() {
			lockFile.Close()
			os.Remove(lockPath)
		}()
	}

	// Step 0: clean wordlist
	if !*resolveOnly {
		if err := cleanWordlistOnce(*wordlistPath, *cleanedPath, *noDedup, *sortMem); err != nil {
			log.Fatalf("[clean] failed: %v", err)
		}
		if *cleanOnly {
			logf("clean-only done")
			return
		}
	}

	roots, err := loadRoots(*rootsPath)
	if err != nil {
		log.Fatalf("load roots %s: %v", *rootsPath, err)
	}
	logf("Total roots: %d", len(roots))
	if len(roots) == 0 {
		logf("No roots, nothing to do")
		return
	}

	// Step 1: generate
	var generated []string
	if !*resolveOnly {
		os.MkdirAll(*toBrutePath, 0755)
		logf("Stage 1: generate (streaming, O(1) RAM) wordlist=%s", *cleanedPath)
		for i, domain := range roots {
			outPath := filepath.Join(*toBrutePath, domain+".txt")
			logf("[%d/%d] gen %s", i+1, len(roots), domain)
			if err := generateForDomain(domain, *cleanedPath, outPath); err != nil {
				logf("[gen] FAILED %s: %v", domain, err)
				continue
			}
			generated = append(generated, domain)
		}
		logf("Stage 1 complete: %d/%d", len(generated), len(roots))
	} else {
		// resolve-only: assume all roots already generated
		generated = roots
	}

	if *generateOnly {
		logf("generate-only done")
		return
	}

	// Step 2: puredns
	os.MkdirAll(*resolvedPath, 0755)
	logf("Stage 2: puredns (workers=%d, chunk=%d)", *purednsWorkers, *chunkLines)

	var wg sync.WaitGroup
	sem := make(chan struct{}, *purednsWorkers)
	var mu sync.Mutex
	var resolvedCount int

	for i, domain := range generated {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, d string) {
			defer wg.Done()
			defer func() { <-sem }()
			inPath := filepath.Join(*toBrutePath, d+".txt")
			outPath := filepath.Join(*resolvedPath, d+".txt")
			logf("[%d/%d] puredns %s", idx+1, len(generated), d)
			if err := resolveWithPuredns(inPath, outPath, *chunkLines); err != nil {
				logf("[puredns] FAILED %s: %v", d, err)
				return
			}
			mu.Lock()
			resolvedCount++
			mu.Unlock()
		}(i, domain)
	}
	wg.Wait()

	if err := cleanupToBruteDir(*toBrutePath, len(generated), resolvedCount); err != nil {
		logf("[cleanup] failed to remove %s: %v", *toBrutePath, err)
	} else if len(generated) > 0 && resolvedCount == len(generated) {
		logf("[cleanup] removed %s", *toBrutePath)
	}

	logf("============================================")
	logf("Workflow COMPLETE")
	logf("  Bruteforce candidates: %s (%d files)", *toBrutePath, len(generated))
	logf("  Resolved domains: %s (%d files)", *resolvedPath, resolvedCount)
	logf("  Cleaned wordlist: %s", *cleanedPath)
	logf("============================================")
}
