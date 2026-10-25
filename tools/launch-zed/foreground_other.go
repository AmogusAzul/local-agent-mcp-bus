//go:build !windows

package main

// foregroundTitle is Windows-only; this stub lets tests build elsewhere.
func foregroundTitle() string { return "" }
