//go:build !windows

package main

import (
	"fmt"
	"net"
)

// listDevicesPlatform 打印系统网络接口（Linux，纯 Go）。
func listDevicesPlatform() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		ips := ""
		for i, a := range addrs {
			if i > 0 {
				ips += ","
			}
			if ipn, ok := a.(*net.IPNet); ok {
				ips += ipn.IP.String()
			}
		}
		fmt.Printf("%s\t%s [%s]\n", ifi.Name, ifi.Name, ips)
	}
	return nil
}
