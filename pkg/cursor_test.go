// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

var _ = Describe("LoadCursor", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("missing path returns non-nil cursor with non-nil empty Repos map", func() {
		c, err := pkg.LoadCursor(ctx, filepath.Join(GinkgoT().TempDir(), "nonexistent"))
		Expect(err).NotTo(HaveOccurred())
		Expect(c).NotTo(BeNil())
		Expect(c.Repos).NotTo(BeNil())
		Expect(c.Repos).To(BeEmpty())
	})

	It("round-trips a known file", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		orig := &pkg.Cursor{
			Repos: map[string]*pkg.RepoState{
				"github.com/bborbe/a": {LastSeenHeadSHA: "abc"},
			},
		}
		Expect(pkg.SaveCursor(ctx, path, orig)).To(Succeed())
		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Repos["github.com/bborbe/a"].LastSeenHeadSHA).To(Equal("abc"))
	})

	It("null repos becomes non-nil empty map", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		Expect(os.WriteFile(path, []byte(`{"repos":null}`), 0600)).To(Succeed())
		c, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Repos).NotTo(BeNil())
		Expect(c.Repos).To(BeEmpty())
	})

	It("non-json cold-starts instead of erroring", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		Expect(os.WriteFile(path, []byte("not json"), 0600)).To(Succeed())
		c, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Repos).To(BeEmpty())
	})

	It("non-json preserves the bad file as .corrupt", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		Expect(os.WriteFile(path, []byte("not json"), 0600)).To(Succeed())
		_, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		saved, err := os.ReadFile(path + ".corrupt")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(saved)).To(Equal("not json"))
	})

	It("non-json does not wedge the next cycle", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		Expect(os.WriteFile(path, []byte("not json"), 0600)).To(Succeed())
		_, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		// The whole point of the change: a second cycle must also succeed.
		// The old behaviour returned an error here forever, because nothing
		// rewrites a cursor file that fails to load.
		_, err = pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("SaveCursor", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("SaveCursor then LoadCursor round-trips", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		c := &pkg.Cursor{
			Repos: map[string]*pkg.RepoState{
				"github.com/bborbe/b": {LastSeenHeadSHA: "def"},
			},
		}
		Expect(pkg.SaveCursor(ctx, path, c)).To(Succeed())
		loaded, err := pkg.LoadCursor(ctx, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Repos["github.com/bborbe/b"].LastSeenHeadSHA).To(Equal("def"))
	})

	It("after save, directory contains cursor file and no .tmp", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "cursor.json")
		c := &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
		Expect(pkg.SaveCursor(ctx, path, c)).To(Succeed())
		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		Expect(names).To(ContainElement("cursor.json"))
		Expect(names).To(Not(ContainElement(ContainSubstring(".tmp"))))
	})

	It("save to non-existent directory returns error and leaves no .tmp", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "nonexistent", "cursor.json")
		c := &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
		err := pkg.SaveCursor(ctx, path, c)
		Expect(err).To(HaveOccurred())
		// No .tmp file should exist anywhere in the temp dir
		tmpFound := false
		entries, rerr := os.ReadDir(dir)
		Expect(rerr).NotTo(HaveOccurred())
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".tmp" {
				tmpFound = true
			}
		}
		Expect(tmpFound).To(BeFalse())
	})

	It("rename failure returns error and leaves no .tmp", func() {
		// Use a path that is an existing non-empty directory to make Rename fail
		dir := GinkgoT().TempDir()
		nonEmptyDir := filepath.Join(dir, "subdir")
		Expect(os.MkdirAll(nonEmptyDir, 0755)).To(Succeed())
		Expect(
			os.WriteFile(filepath.Join(nonEmptyDir, "existing"), []byte("x"), 0644),
		).To(Succeed())
		path := nonEmptyDir // Rename to a directory that already exists as a file destination
		c := &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
		err := pkg.SaveCursor(ctx, path, c)
		Expect(err).To(HaveOccurred())
		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(e.Name()).NotTo(ContainSubstring(".tmp"))
		}
	})

	It("written file has mode 0600", func() {
		path := filepath.Join(GinkgoT().TempDir(), "cursor.json")
		c := &pkg.Cursor{Repos: map[string]*pkg.RepoState{}}
		Expect(pkg.SaveCursor(ctx, path, c)).To(Succeed())
		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)))
	})
})
