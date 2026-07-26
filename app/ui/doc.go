// Package ui renders a live view of a review run in the terminal.
//
// It does no OS work: no subprocess, no file access, no network, and no writes to stdout. The
// pipeline runs headless and emits typed events; this package is one subscriber of them and the plain
// renderer in package main is the other. Everything it needs arrives on ModelConfig.
package ui
