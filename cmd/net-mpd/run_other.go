//go:build !darwin

package main

func run(f func()) {
	f()
}
