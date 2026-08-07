package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeSubgenRecord(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		domain string
		want   string
	}{
		{name: "basic", input: "www", domain: "Example.COM", want: "www.example.com"},
		{name: "invalid characters", input: "foo_bar!baz", domain: "example.com", want: "foobarbaz.example.com"},
		{name: "blank input", input: "", domain: "example.com", want: ".example.com"},
		{name: "invalid domain characters", input: "api", domain: "Example_One.COM", want: "api.exampleone.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizeSubgenRecord(test.input, test.domain); got != test.want {
				t.Fatalf("sanitizeSubgenRecord() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunSubgenModePreservesOrderAndDuplicates(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("WWW\napi\nWWW\n")

	if err := runSubgenMode("Example.COM", input, &output); err != nil {
		t.Fatalf("runSubgenMode() error = %v", err)
	}

	want := "www.example.com\napi.example.com\nwww.example.com\n"
	if output.String() != want {
		t.Fatalf("runSubgenMode() output = %q, want %q", output.String(), want)
	}
}

func TestGenerateForDomain(t *testing.T) {
	dir := t.TempDir()
	wordlist := filepath.Join(dir, "cleaned.wordlist")
	output := filepath.Join(dir, "to-brute", "example.com.txt")
	if err := os.WriteFile(wordlist, []byte("www\napi\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := generateForDomain("example.com", wordlist, output); err != nil {
		t.Fatalf("generateForDomain() error = %v", err)
	}

	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("www.example.com\napi.example.com\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("generated output = %q, want %q", got, want)
	}
}

func TestResolveWithPurednsChunkedOffline(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	puredns := filepath.Join(binDir, "puredns")
	script := "#!/bin/sh\nif [ \"$1\" != resolve ] || [ \"$3\" != --write ]; then exit 2; fi\ncp \"$2\" \"$4\"\n"
	if err := os.WriteFile(puredns, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	input := filepath.Join(dir, "input.txt")
	output := filepath.Join(dir, "resolved.txt")
	want := "one.example.test\ntwo.example.test\nthree.example.test\n"
	if err := os.WriteFile(input, []byte(want), 0644); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	if err := resolveWithPuredns(input, output, 1); err != nil {
		t.Fatalf("resolveWithPuredns() error = %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(string(got), want) {
		t.Fatalf("resolved output = %q, want %q", got, want)
	}
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("resolved input still exists, stat error = %v", err)
	}
}

func TestResolveWithPurednsFailurePreservesInput(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	puredns := filepath.Join(binDir, "puredns")
	if err := os.WriteFile(puredns, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	input := filepath.Join(dir, "input.txt")
	output := filepath.Join(dir, "resolved.txt")
	if err := os.WriteFile(input, []byte("one.example.test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	if err := resolveWithPuredns(input, output, 500000); err == nil {
		t.Fatal("resolveWithPuredns() succeeded, want error")
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatalf("resolved input was not preserved: %v", err)
	}
}

func TestCleanupToBruteDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "candidate.txt"), []byte("candidate"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := cleanupToBruteDir(dir, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory removed after incomplete resolution: %v", err)
	}

	if err := cleanupToBruteDir(dir, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still exists after complete resolution, stat error = %v", err)
	}
}
