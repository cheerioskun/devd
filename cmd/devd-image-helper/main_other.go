//go:build !linux

package main

import "fmt"

func main() {
	fmt.Println("devd-image-helper runs only inside a Linux helper VM")
}
