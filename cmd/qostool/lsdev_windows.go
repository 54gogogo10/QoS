//go:build windows

package main

import (
	"fmt"

	"github.com/gopacket/gopacket/pcap"
)

// listDevicesPlatform 打印 pcap 可用的接口（Windows 上是 GUID 名，帮助用户选 -i）。
func listDevicesPlatform() error {
	devs, err := pcap.FindAllDevs()
	if err != nil {
		return err
	}
	for _, d := range devs {
		fmt.Printf("%s\t%s\n", d.Name, d.Description)
	}
	return nil
}
