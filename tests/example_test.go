// Copyright 2016-2023, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tests

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type parsedExample struct {
	cloneURL string
	org      string
	repo     string
	path     string
}

func parseExample(t *testing.T, v string) parsedExample {
	t.Helper()
	require.True(t, strings.HasPrefix(v, "https://github.com/"))
	trimmed := strings.TrimPrefix(v, "https://github.com/")
	segments := strings.Split(trimmed, "/")
	require.True(t, len(segments) >= 2)

	var path string
	if len(segments) > 2 {
		path = filepath.Join(segments[2:]...)
	}

	return parsedExample{
		cloneURL: fmt.Sprintf("https://github.com/%s/%s.git", segments[0], segments[1]),
		org:      segments[0],
		repo:     segments[1],
		path:     path,
	}
}

type keyedMutex struct {
	mutexes sync.Map
}

func (m *keyedMutex) Lock(key string) func() {
	value, _ := m.mutexes.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return func() { mutex.Unlock() }
}

type stringSet map[string]struct{}

func newStringSet(values ...string) stringSet {
	s := stringSet{}
	for _, v := range values {
		s[v] = struct{}{}
	}
	return s
}

func (ss stringSet) Has(s string) bool {
	_, ok := ss[s]
	return ok
}

func (ss stringSet) Equal(other stringSet) bool {
	if len(ss) != len(other) {
		return false
	}
	for k := range ss {
		if !other.Has(k) {
			return false
		}
	}
	return true
}

const (
	csharp     = "c#"
	golang     = "go"
	python     = "python"
	typescript = "typescript"
)

var allLanguages = newStringSet(csharp, golang, python, typescript)

func TestExample(t *testing.T) {
	t.Parallel()

	km := keyedMutex{}

	languages := []string{
		csharp,
		golang,
		python,
		typescript,
	}

	tests := []struct {
		example string
		strict  bool
		skip    stringSet
	}{
		{
			example: "https://github.com/terraform-aws-modules/terraform-aws-iam/examples/iam-eks-role",
			// TODO[pulumi/pulumi-converter-terraform#32]: upstream example change can no longer convert
			// Was incidentally broken by https://github.com/pulumi/pulumi-converter-terraform/pull/91.
		},
		{
			example: "https://github.com/terraform-aws-modules/terraform-aws-iam/examples/iam-role-for-service-accounts-eks",
			// TODO[pulumi/pulumi-converter-terraform#32]: upstream example change can no longer convert
		},
		{
			example: "https://github.com/terraform-aws-modules/terraform-aws-acm",
			strict:  true,
			// TODO[pulumi/pulumi-converter-terraform#32]: upstream example change can no longer convert
		},
		{
			example: "https://github.com/aws-ia/terraform-aws-eks-blueprints/patterns/multi-tenancy-with-teams",
			// TODO[pulumi/pulumi-converter-terraform#32]: upstream example change can no longer convert
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.example, func(t *testing.T) {
			t.Parallel()

			if tt.skip.Equal(allLanguages) {
				t.Skip()
			}

			parsed := parseExample(t, tt.example)

			orgDir := filepath.Join("repos", parsed.org)
			require.NoError(t, os.MkdirAll(orgDir, 0o700), "creating repo org directory")
			repoDir := filepath.Join(orgDir, parsed.repo)

			// Clone the repo locally, if it doesn't already exist.
			unlock := km.Lock(repoDir)
			if _, err := os.Stat(repoDir); os.IsNotExist(err) {
				_, _, err = runCommand(t, orgDir, "git", "clone", "--depth", "1", parsed.cloneURL)
				require.NoError(t, err, "cloning repo")
			}
			unlock()

			// Test each language.
			exampleDir := filepath.Join(repoDir, parsed.path)
			for _, language := range languages {
				language := language
				t.Run(language, func(t *testing.T) {
					t.Parallel()

					if tt.skip.Has(language) {
						t.Skip()
					}

					testExample(t, exampleDir, language, tt.strict)
				})
			}
		})
	}
}

func testExample(t *testing.T, path, language string, strict bool) {
	outputDir, err := os.MkdirTemp("", "converter-output")
	require.NoError(t, err, "creating temp directory for test")
	defer func() {
		if !t.Failed() {
			err := os.RemoveAll(outputDir)
			require.NoErrorf(t, err, "cleaning up temp test directory %q", outputDir)
		}
	}()

	args := []string{
		"convert",
		"--generate-only",
		"--from", "terraform",
		"--language", language,
		"--out", outputDir,
	}
	if strict {
		args = append(args, "--strict")
	}

	stdout, stderr, err := runCommand(t, path, "pulumi", args...)
	if err != nil {
		t.Logf("Command failed: %s", err)
		t.Logf("STDOUT: %s", stdout)
		t.Logf("STDERR: %s", stderr)
		t.FailNow()
	}

	if language == "typescript" {
		logNotImplementedReport(t, outputDir, ".ts")
	}
}

func runCommand(t *testing.T, cwd, command string, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	cmd := exec.Command(command, args...)
	cmd.Dir = cwd
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Logf("Running command: %s %s", command, strings.Join(args, " "))
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func logNotImplementedReport(t *testing.T, path, extension string) {
	report := notImplementedReport(t, path, extension)

	type Pair struct {
		Key   string
		Value int
	}

	pairs := make([]Pair, len(report))
	i := 0
	for k, v := range report {
		pairs[i] = Pair{k, v}
		i++
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Value > pairs[j].Value
	})

	var total int
	for _, p := range pairs {
		total += p.Value
	}
	if total > 0 {
		t.Logf("%d total notImplemented", total)
	}
	for _, p := range pairs {
		t.Logf("notImplemented (%v): %s\n", p.Value, p.Key)
	}
}

func notImplementedReport(t *testing.T, path, extension string) map[string]int {
	result := make(map[string]int)

	regex := regexp.MustCompile(`(?mU)notImplemented\([\x60"](.*)[\x60"(]`)

	files, err := filepath.Glob(filepath.Join(path, "*"+extension))
	require.NoError(t, err, "globbing files")

	for _, file := range files {
		contents, err := os.ReadFile(file)
		require.NoError(t, err, "reading file %q", file)

		matches := regex.FindAllStringSubmatch(string(contents), -1)
		if len(matches) > 0 {
			for _, match := range matches {
				result[match[1]]++
			}
		}
	}

	return result
}
